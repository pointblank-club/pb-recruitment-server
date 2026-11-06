require "minitest/autorun"
require "securerandom"

raise "disposable privileged Judge0 runner required" unless ENV["COMPILATION_SANDBOX_TEST"] == "1"

class CompilationSandboxTest < Minitest::Test
  CPP = "// café\n#include <iostream>\nint main(){int a,b;std::cin>>a>>b;std::cout<<a+b<<std::endl;}"
  JAVA = "// café\nimport java.util.Scanner; public class Main {public static void main(String[] a){Scanner s=new Scanner(System.in);System.out.println(s.nextInt()+s.nextInt());}}"

  def setup
    @ids = []; @groups = []
  end

  def teardown
    Submission.unscoped.where(id: @ids).delete_all
    @groups.each do |group|
      ActiveRecord::Base.connection.raw_connection.exec_params("DELETE FROM compilation_artifacts WHERE compilation_group=$1", [group])
    end
  end

  def execute_cases(language, source, cases, expected, grouped: true)
    group = grouped ? SecureRandom.uuid : nil
    @groups << group if group
    submissions = cases.map do |input, output|
      value = Submission.create!(source_code: source, language_id: language,
        compilation_group: group, stdin: input, expected_output: output,
        cpu_time_limit: 1, wall_time_limit: 5, memory_limit: 4096000,
        enable_per_process_and_thread_time_limit: true,
        enable_per_process_and_thread_memory_limit: true, enable_network: false)
      @ids << value.id
      value
    end
    log, = capture_io { submissions.each { |value| IsolateJob.perform_now(value.id) } }
    submissions.zip(expected).each do |value, verdict|
      value.reload
      assert_equal verdict, value.status_id, "language #{language}: #{value.message} #{value.compile_output} #{value.stderr} #{value.stdout}"
      assert value.finished_at
    end
    assert_equal(grouped ? 1 : cases.length, log.scan(/Compiling submission /).length, log)
  end

  def test_cpp_and_java_compile_once_and_keep_independent_verdicts
    [ [54, CPP], [62, JAVA] ].each do |language, source|
      execute_cases(language, source, [["1 3\n", "4\n"], ["10 20\n", "30\n"], ["-4 2\n", "0\n"]], [3, 3, 4])
    end
  end

  def test_compilation_runtime_and_time_limit_errors_remain_native
    execute_cases(54, "this cannot compile", [["", ""], ["", ""]], [6, 6])
    execute_cases(54, "int main(){return 7;}", [["", ""], ["", ""]], [11, 11])
    execute_cases(54, "int main(){for(;;){}}", [["", ""], ["", ""]], [5, 5])
  end

  def test_legacy_jobs_still_compile_individually
    execute_cases(54, CPP, [["1 2\n", "3\n"], ["3 4\n", "7\n"]], [3, 3], grouped: false)
    execute_cases(62, JAVA, [["1 2\n", "3\n"]], [3], grouped: false)
    execute_cases(54, "this cannot compile", [["", ""]], [6], grouped: false)
  end
end
