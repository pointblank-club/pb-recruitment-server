require "minitest/autorun"
require "active_record"
require "pg"
require "uri"
require "securerandom"
require "tmpdir"
require "open3"
require_relative "../lib/compilation_cache"
require_relative "../db/migrate/20261107000000_add_compilation_groups"

# Standalone integration harness; never loads the recruitment runtime credentials.
unless defined?(Config)
  module Config
    MAX_CPU_TIME_LIMIT = 15; MAX_WALL_TIME_LIMIT = 20; MAX_STACK_LIMIT = 128000
    MAX_MAX_PROCESSES_AND_OR_THREADS = 120; MAX_MEMORY_LIMIT = 512000; MAX_MAX_FILE_SIZE = 4096
  end
end
unless defined?(Status)
  module Status
    def self.ce; 6; end
  end
end

class CacheTestSubmission < ActiveRecord::Base
  self.table_name = "submissions"
  attr_accessor :language, :language_id, :source_code, :compiler_options,
    :enable_per_process_and_thread_time_limit, :enable_per_process_and_thread_memory_limit
  def additional_files?; false; end
  def status=(value); self.status_id = value.respond_to?(:id) ? value.id : value; end
end

class CompilationCachePostgresTest < Minitest::Test
  def setup
    url = ENV["COMPILATION_TEST_DATABASE_URL"]
    skip "explicit disposable PostgreSQL URL required" unless url
    uri = URI.parse(url)
    parameters = URI.decode_www_form(uri.query || "").to_h
    host = parameters["host"] || uri.host
    raise "local disposable PostgreSQL required" unless ["postgres", "postgresql"].include?(uri.scheme) && host && (host.start_with?("/") || ["localhost", "127.0.0.1"].include?(host))
    @schema = "compile_cache_test_" + SecureRandom.hex(8)
    @admin = PG.connect(url)
    @admin.exec("CREATE SCHEMA #{@schema}")
    ActiveRecord::Base.establish_connection(adapter: "postgresql", host: host,
      port: parameters["port"] || uri.port, database: URI::DEFAULT_PARSER.unescape(uri.path.sub(%r{\A/}, "")),
      username: uri.user && URI::DEFAULT_PARSER.unescape(uri.user),
      password: uri.password && URI::DEFAULT_PARSER.unescape(uri.password),
      schema_search_path: "#{@schema},public", pool: 12)
    @previous_build = ENV["JUDGE0_COMPILER_BUILD_ID"]
    ENV["JUDGE0_COMPILER_BUILD_ID"] = "integration-test-toolchain"
    ActiveRecord::Migration.verbose = false
    ActiveRecord::Schema.define do
      create_table :submissions do |t|
        t.integer :status_id, default: 2
        t.text :compile_output; t.datetime :finished_at
        t.float :time; t.float :wall_time; t.integer :memory
        t.text :stdout; t.text :stderr; t.integer :exit_code; t.integer :exit_signal; t.text :message
      end
    end
    AddCompilationGroups.new.migrate(:up)
    CacheTestSubmission.reset_column_information
  end

  def teardown
    return unless @admin
    ENV["JUDGE0_COMPILER_BUILD_ID"] = @previous_build
    ActiveRecord::Base.connection_pool.disconnect!
    @admin.exec("DROP SCHEMA #{@schema} CASCADE")
    @admin.close
  end

  def submission(group = SecureRandom.uuid, language = 54)
    value = CacheTestSubmission.create!(compilation_group: group)
    value.language_id = language
    value.language = Struct.new(:compile_cmd, :source_file, :run_cmd).new("compiler", "source", "program")
    value.source_code = "source"
    value.compiler_options = ""
    value.enable_per_process_and_thread_time_limit = false
    value.enable_per_process_and_thread_memory_limit = false
    value
  end

  def worker(value, directory)
    Struct.new(:submission, :boxdir).new(value, directory)
  end

  def test_concurrent_cases_compile_once
    group = SecureRandom.uuid
    count = 0; mutex = Mutex.new
    # Each worker holds a different connection, as separate Judge0 processes do.
    threads = 10.times.map do
      Thread.new do
        ActiveRecord::Base.connection_pool.with_connection do
          value = submission(group)
          Dir.mktmpdir do |directory|
            result = CompilationCache.fetch(worker(value, directory)) do
              mutex.synchronize { count += 1 }
              sleep 0.1
              File.write(File.join(directory, "program"), "compiled")
              :success
            end
            raise "missing artifact" unless File.read(File.join(directory, "program")) == "compiled"
            result
          end
        end
      end
    end
    assert_equal [:success] * 10, threads.map(&:value)
    assert_equal 1, count
    assert_equal 1, @admin.exec("SELECT count(*) FROM #{@schema}.compilation_artifacts").first["count"].to_i
  end

  def test_worker_hook_reuses_group_and_leaves_legacy_compilation_unchanged
    fixture = Class.new do
      attr_reader :submission, :boxdir
      def initialize(value, directory, counter); @submission=value; @boxdir=directory; @counter=counter; end
      def compile; @counter.call; File.write(File.join(boxdir, "program"), "compiled"); :success; end
      prepend CompileOnce
    end
    group = SecureRandom.uuid; count = 0
    3.times do
      Dir.mktmpdir do |directory|
        assert_equal :success, fixture.new(submission(group), directory, -> {count += 1}).send(:compile)
      end
    end
    assert_equal 1, count
    2.times do
      value = submission; value.compilation_group = nil
      Dir.mktmpdir { |directory| fixture.new(value, directory, -> {count += 1}).send(:compile) }
    end
    assert_equal 3, count
  end

  def test_failure_reused_and_source_mismatch_rejected
    group = SecureRandom.uuid
    Dir.mktmpdir do |directory|
      first = submission(group)
      CompilationCache.fetch(worker(first, directory)) { first.compile_output = "compile error"; :failure }
      second = submission(group)
      assert_equal :failure, CompilationCache.fetch(worker(second, directory)) { flunk "recompiled failure" }
      assert_equal 6, second.reload.status_id
      assert_equal "compile error", second.compile_output
      third = submission(group); third.source_code = "changed"
      assert_raises(RuntimeError) { CompilationCache.fetch(worker(third, directory)) { flunk "accepted changed source" } }
    end
  end

  def test_rollback_releases_lock_and_does_not_publish_artifact
    value = submission
    Dir.mktmpdir do |directory|
      assert_raises(RuntimeError) { CompilationCache.fetch(worker(value, directory)) { raise "compiler interrupted" } }
      assert_equal :success, CompilationCache.fetch(worker(value, directory)) { File.write(File.join(directory, "program"), "ok"); :success }
    end
  end

  def test_expiry_preserves_pending_groups
    pending = submission; finished = submission; finished.update!(status_id: 3)
    connection = ActiveRecord::Base.connection.raw_connection
    connection.exec("SET TIME ZONE 'Asia/Kolkata'")
    [pending, finished].each do |value|
      Dir.mktmpdir { |directory| CompilationCache.fetch(worker(value, directory)) { :success } }
    end
    @admin.exec("UPDATE #{@schema}.compilation_artifacts SET created_at=created_at-interval '25 hours'")
    connection.exec("SET TIME ZONE 'UTC'")
    CompilationCache.expire
    rows = @admin.exec("SELECT compilation_group FROM #{@schema}.compilation_artifacts").map { |row| row["compilation_group"] }
    assert_equal [pending.compilation_group], rows
  end

  def test_real_cpp_and_java_artifacts_run_with_distinct_inputs
    cases = [
      [54, "Main.cpp", "#include <iostream>\nint main(){int a,b;std::cin>>a>>b;std::cout<<a+b<<std::endl;}", ["g++", "Main.cpp", "-o", "program"], ["./program"]],
      [62, "Main.java", "import java.util.Scanner; public class Main {public static void main(String[] a){Scanner s=new Scanner(System.in);System.out.println(s.nextInt()+s.nextInt());}}", ["javac", "Main.java"], ["java", "Main"]]
    ]
    cases.each do |language, filename, source, compile, execute|
      if defined?(Language)
        native = Language.find(language)
        filename = native.source_file
        compile = ["bash", "-c", native.compile_cmd % ""]
        execute = ["bash", "-c", native.run_cmd]
      end
      group = SecureRandom.uuid; compilations = 0
      [[1, 3], [10, 20], [-4, 2]].each do |a,b|
        value = submission(group, language); value.source_code = source
        Dir.mktmpdir do |directory|
          result = CompilationCache.fetch(worker(value, directory)) do
            compilations += 1
            File.write(File.join(directory, filename), source)
            _, error, status = Open3.capture3(*compile, chdir: directory)
            raise error unless status.success?
            :success
          end
          assert_equal :success, result
          output, error, status = Open3.capture3(*execute, stdin_data: "#{a} #{b}\n", chdir: directory)
          assert status.success?, error
          assert_equal "#{a+b}\n", output
        end
      end
      assert_equal 1, compilations
    end
  end
end
