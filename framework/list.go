// Package framework provides a list of dependencies that are required for the framework to work.
package framework

// FrameworkDependency is a type that represents a dependency of the framework.
type FrameworkDependency string

const (
	// FrameworkDependencyVectorStore indicates the framework requires a VectorStore implementation.
	FrameworkDependencyVectorStore FrameworkDependency = "vector_store"
	// FrameworkDependencyConfigStore indicates the framework requires a ConfigStore implementation.
	FrameworkDependencyConfigStore FrameworkDependency = "config_store"
	// FrameworkDependencyLogsStore indicates the framework requires a LogsStore implementation.
	FrameworkDependencyLogsStore FrameworkDependency = "logs_store"
)
