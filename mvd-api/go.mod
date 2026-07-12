module github.com/mvd-analyzer/mvd-api

go 1.25.0

require (
	github.com/google/jsonschema-go v0.4.3
	github.com/mvd-analyzer/mvd-analytics v0.0.0
	gopkg.in/yaml.v3 v3.0.1
)

require github.com/mvd-analyzer/mvd-reader v0.0.0 // indirect

replace (
	github.com/mvd-analyzer/mvd-analytics => ../mvd-analytics
	github.com/mvd-analyzer/mvd-reader => ../mvd-reader
)
