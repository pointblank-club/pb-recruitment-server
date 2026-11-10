require "minitest/autorun"
require "securerandom"

raise "run using the patched Judge0 Rails runner" unless defined?(SubmissionsController)

class CompilationGroupContractTest < Minitest::Test
  def attributes(language = 54, group = SecureRandom.uuid)
    params = ActionController::Parameters.new(source_code: "int main() {}", language_id: language, compilation_group: group)
    controller = SubmissionsController.new
    controller.send(:submission_params, params)
  end

  def test_controller_preserves_group_and_model_accepts_cpp_and_java
    [54, 62].each do |language|
      group = SecureRandom.uuid
      values = attributes(language, group)
      assert_equal group, values[:compilation_group]
      candidate = Submission.new(values)
      assert candidate.valid?, candidate.errors.full_messages.join(", ")
    end
  end

  def test_legacy_requests_remain_valid
    candidate = Submission.new(attributes(54, nil))
    assert candidate.valid?, candidate.errors.full_messages.join(", ")
  end

  def test_group_rejects_bad_identifier_and_unsupported_language
    [attributes(54, "not-a-uuid"), attributes(71)].each do |values|
      candidate = Submission.new(values)
      refute candidate.valid?
      assert candidate.errors[:compilation_group].any?
    end
  end

  def test_additional_files_cannot_use_compilation_groups
    candidate = Submission.new(attributes.merge(additional_files: "project"))
    refute candidate.valid?
    assert candidate.errors[:compilation_group].any?
  end
end
