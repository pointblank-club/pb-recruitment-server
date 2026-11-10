require Rails.root.join("lib/compilation_cache")

module CompilationGroupParameters
  private
  def submission_params(params)
    super.tap { |attributes| attributes[:compilation_group] = params[:compilation_group] }
  end
end

Rails.application.config.to_prepare do
  unless Submission.validators_on(:compilation_group).any?
    Submission.validates :compilation_group, format: { with: /\A[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}\z/i }, allow_nil: true
    Submission.validate do
      if compilation_group.present? && (![54, 62].include?(language_id) || additional_files?)
        errors.add(:compilation_group, "requires C++/Java without additional files")
      end
    end
  end
  SubmissionsController.prepend(CompilationGroupParameters) unless SubmissionsController < CompilationGroupParameters
  IsolateJob.prepend(CompileOnce) unless IsolateJob < CompileOnce
end
