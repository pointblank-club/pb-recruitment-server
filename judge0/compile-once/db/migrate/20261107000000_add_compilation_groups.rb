class AddCompilationGroups < ActiveRecord::Migration[5.0]
  def change
    add_column :submissions, :compilation_group, :string, limit: 36
    add_index :submissions, :compilation_group
    create_table :compilation_artifacts, id: false do |t|
      t.string :compilation_group, limit: 36, primary_key: true
      t.string :fingerprint, limit: 64, null: false
      t.text :payload, null: false
      t.datetime :created_at, null: false
    end
    add_index :compilation_artifacts, :created_at
  end
end
