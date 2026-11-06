require "base64"
require "digest"
require "fileutils"
require "find"
require "json"
require "pathname"

# Runs in the trusted worker, outside the candidate's isolate sandbox.
module CompilationCache
  MAX_BYTES = 8 * 1024 * 1024
  MAX_FILES = 256
  class Uncacheable < StandardError; end

  def self.fingerprint(submission)
    build = ENV.fetch("JUDGE0_COMPILER_BUILD_ID")
    raise "compiler build identity must be nonempty" if build.empty?
    Digest::SHA256.hexdigest(JSON.generate([
      build, submission.language_id, submission.language.compile_cmd,
      submission.language.source_file, submission.language.run_cmd,
      submission.source_code, submission.compiler_options,
      submission.enable_per_process_and_thread_time_limit,
      submission.enable_per_process_and_thread_memory_limit,
      Config::MAX_CPU_TIME_LIMIT, Config::MAX_WALL_TIME_LIMIT,
      Config::MAX_STACK_LIMIT, Config::MAX_MAX_PROCESSES_AND_OR_THREADS,
      Config::MAX_MEMORY_LIMIT, Config::MAX_MAX_FILE_SIZE,
      ENV.values_at("LANG", "LANGUAGE", "LC_ALL")
    ]))
  end

  def self.capture(directory)
    files = []; size = 0; entries = 0
    Find.find(directory) do |path|
      entries += 1
      raise Uncacheable if entries > MAX_FILES + 1
      info = File.lstat(path)
      next if info.directory?
      raise Uncacheable unless info.file? && !info.symlink?
      size += info.size
      raise Uncacheable if size > MAX_BYTES || files.length >= MAX_FILES
      data = File.open(path, File::RDONLY | File::NOFOLLOW) { |f| f.binmode; f.read(MAX_BYTES + 1) || "".b }
      raise Uncacheable if data.bytesize != info.size
      name = Pathname.new(path).relative_path_from(Pathname.new(directory)).to_s
      files << { "path" => name, "mode" => info.mode & 0777, "data" => Base64.strict_encode64(data) }
    end
    files
  rescue SystemCallError
    raise Uncacheable
  end

  def self.restore(directory, files)
    raise "invalid artifact manifest" unless files.is_a?(Array) && files.length <= MAX_FILES
    size = 0; names = []
    decoded = files.map do |file|
      name = file.fetch("path")
      parts = name.split("/", -1)
      raise "unsafe artifact path" if Pathname.new(name).absolute? || name.include?("\0") || parts.any? { |p| ["", ".", ".."].include?(p) }
      raise "duplicate artifact path" if names.include?(name)
      names << name
      data = Base64.strict_decode64(file.fetch("data"))
      size += data.bytesize
      mode = file.fetch("mode")
      raise "invalid artifact limits" if size > MAX_BYTES || !mode.is_a?(Integer) || mode < 0 || mode > 0777
      [name, mode, data]
    end
    raise "conflicting artifact paths" if names.any? { |name| names.any? { |other| other.start_with?(name + "/") } }
    # Each case gets a fresh copy; no candidate can modify another case's artifact.
    Dir.children(directory).each { |name| FileUtils.rm_rf(File.join(directory, name)) }
    decoded.each do |name, mode, data|
      path = File.join(directory, name)
      FileUtils.mkdir_p(File.dirname(path))
      File.open(path, File::WRONLY | File::CREAT | File::EXCL | File::NOFOLLOW, mode) { |f| f.binmode; f.write(data) }
      File.chmod(mode, path)
    end
  end

  def self.apply_failure(submission, output)
    submission.update!(compile_output: output, status: Status.ce, finished_at: DateTime.now,
      time: nil, wall_time: nil, memory: nil, stdout: nil, stderr: nil,
      exit_code: nil, exit_signal: nil, message: nil)
    :failure
  end

  def self.fetch(worker)
    submission = worker.submission
    fingerprint = fingerprint(submission)
    group = submission.compilation_group
    # The transaction-scoped lock works across worker processes and hosts.
    key = Digest::SHA256.digest(group).unpack("q>").first
    ActiveRecord::Base.transaction do
      connection = ActiveRecord::Base.connection.raw_connection
      # Use parameterized PG calls so SQL debug logs never dump artifact/source bytes.
      connection.exec_params("SELECT pg_advisory_xact_lock($1::bigint)", [key])
      row = connection.exec_params("SELECT fingerprint,payload FROM compilation_artifacts WHERE compilation_group=$1", [group]).first
      if row
        raise "compilation group fingerprint mismatch" unless row.fetch("fingerprint") == fingerprint
        payload = JSON.parse(row.fetch("payload"))
        output = payload.fetch("compile_output_base64")
        output = Base64.strict_decode64(output) if output
        if payload.fetch("result") == "success"
          restore(worker.boxdir, payload.fetch("files"))
          submission.compile_output = output
          :success
        elsif payload.fetch("result") == "failure"
          apply_failure(submission, output)
        else
          raise "invalid compilation result"
        end
      else
        result = yield
        begin
          output = submission.compile_output
          raise Uncacheable if output && output.bytesize > MAX_BYTES
          payload = { "result" => result.to_s, "compile_output_base64" => output && Base64.strict_encode64(output) }
          payload["files"] = capture(worker.boxdir) if result == :success
          connection.exec_params("INSERT INTO compilation_artifacts(compilation_group,fingerprint,payload,created_at) VALUES($1,$2,$3,timezone('UTC',now()))", [group, fingerprint, JSON.generate(payload)])
        rescue Uncacheable, JSON::GeneratorError
          # shortcut: oversized/nonregular artifacts compile normally; use a bounded artifact store before widening support.
        end
        result
      end
    end
  end

  def self.expire
    # Retain artifacts for pending jobs; bound housekeeping work per invocation.
    ActiveRecord::Base.connection.raw_connection.exec_params(<<~SQL, [])
      DELETE FROM compilation_artifacts WHERE compilation_group IN (
        SELECT a.compilation_group FROM compilation_artifacts a
        WHERE a.created_at < timezone('UTC',now()) - interval '24 hours'
        AND NOT EXISTS (SELECT 1 FROM submissions s WHERE s.compilation_group=a.compilation_group AND s.status_id IN (1,2))
        ORDER BY a.created_at LIMIT 100
      )
    SQL
  end
end

module CompileOnce
  private
  def compile
    return super unless submission.compilation_group.present?
    raise "unsupported compilation group" unless [54, 62].include?(submission.language_id) && !submission.additional_files?
    result = CompilationCache.fetch(self) { super }
    result
  ensure
    # Cache maintenance must not change a candidate's verdict if storage is unavailable.
    if submission.compilation_group.present?
      begin
        CompilationCache.expire
      rescue StandardError
        Rails.logger.warn("Compilation artifact expiry failed")
      end
    end
  end
end
