require "minitest/autorun"
require "tmpdir"
require_relative "../lib/compilation_cache"

class CompilationCacheTest < Minitest::Test
  def test_fresh_copy_preserves_executable_and_resets_candidate_writes
    Dir.mktmpdir do |source|
      File.write(File.join(source, "program"), "compiled bytes")
      File.write(File.join(source, "empty"), "")
      File.chmod(0755, File.join(source, "program"))
      artifact = CompilationCache.capture(source)
      Dir.mktmpdir do |target|
        CompilationCache.restore(target, artifact)
        File.write(File.join(target, "program"), "candidate mutation")
        CompilationCache.restore(target, artifact)
        assert_equal "compiled bytes", File.read(File.join(target, "program"))
        assert_equal "", File.read(File.join(target, "empty"))
        assert_equal 0755, File.stat(File.join(target, "program")).mode & 0777
      end
    end
  end

  def test_symlinks_are_not_cached
    Dir.mktmpdir do |directory|
      File.symlink("/etc/passwd", File.join(directory, "link"))
      assert_raises(CompilationCache::Uncacheable) { CompilationCache.capture(directory) }
    end
  end

  def test_unsafe_manifest_cannot_overwrite_or_delete_existing_files
    Dir.mktmpdir do |directory|
      File.write(File.join(directory, "keep"), "keep")
      ["../escape", "/absolute", "a/../escape"].each do |path|
        assert_raises(RuntimeError) do
          CompilationCache.restore(directory, [{"path" => path, "mode" => 0644, "data" => ""}])
        end
        assert_equal "keep", File.read(File.join(directory, "keep"))
      end
    end
  end
end
