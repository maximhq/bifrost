// Package framework provides a list of dependencies that are required for the framework to work.
package framework

// FrameworkDependency is a type that represents a dependency of the framework.
type FrameworkDependency string

const (
	FrameworkDependencyVectorStore FrameworkDependency = "vector_store" // Vector store dependency
	FrameworkDependencyConfigStore FrameworkDependency = "config_store" // Config store dependency
	FrameworkDependencyLogsStore   FrameworkDependency = "logs_store"   // Logs store dependency
)
