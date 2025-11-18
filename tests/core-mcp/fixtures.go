package mcp

// CodeFixtures contains sample JavaScript code snippets for testing
var CodeFixtures = struct {
	// Basic expressions
	SimpleExpression     string
	SimpleString         string
	VariableAssignment   string
	ConsoleLogging       string
	ExplicitReturn       string
	AutoReturnExpression string

	// MCP tool calls
	SingleToolCall          string
	ToolCallWithPromise     string
	ToolCallChain           string
	ToolCallErrorHandling   string
	MultipleServerToolCalls string
	ToolCallWithComplexArgs string

	// Import/Export
	ImportStatement          string
	ExportStatement          string
	MultipleImportExport     string
	ImportExportWithComments string

	// Expression analysis
	FunctionCallExpression  string
	PromiseChainExpression  string
	ObjectLiteralExpression string
	AssignmentStatement     string
	ControlFlowStatement    string
	TopLevelReturn          string

	// Error cases
	UndefinedVariable string
	UndefinedServer   string
	UndefinedTool     string
	SyntaxError       string
	RuntimeError      string

	// Edge cases
	NestedPromiseChains   string
	PromiseErrorHandling  string
	ComplexDataStructures string
	MultiLineExpression   string
	EmptyCode             string
	WhitespaceOnly        string
	CommentsOnly          string
	FunctionDefinition    string

	// Environment tests
	BrowserAPITest  string
	NodeAPITest     string
	AsyncAwaitTest  string
	EnvironmentTest string
}{
	SimpleExpression:     `1 + 1`,
	SimpleString:         `"hello"`,
	VariableAssignment:   `var x = 5; x`,
	ConsoleLogging:       `console.log("test"); "logged"`,
	ExplicitReturn:       `return 42`,
	AutoReturnExpression: `2 + 2`,

	SingleToolCall:          `BifrostClient.echo({message: "hello"})`,
	ToolCallWithPromise:     `BifrostClient.echo({message: "test"}).then(result => { console.log(result); return result; })`,
	ToolCallChain:           `BifrostClient.add({a: 1, b: 2}).then(result => BifrostClient.multiply({a: result, b: 3}))`,
	ToolCallErrorHandling:   `BifrostClient.error_tool({}).catch(err => { console.error(err); return "handled"; })`,
	MultipleServerToolCalls: `BifrostClient.echo({message: "test"}).then(() => BifrostClient.add({a: 1, b: 2}))`,
	ToolCallWithComplexArgs: `BifrostClient.complex_args_tool({data: {nested: {value: 42}}})`,

	ImportStatement:          `import { something } from "module"; 1 + 1`,
	ExportStatement:          `export const x = 5; x`,
	MultipleImportExport:     `import a from "a"; import b from "b"; export const c = 1; 2 + 2`,
	ImportExportWithComments: `// comment\nimport x from "x";\n// another comment\n2 + 2`,

	FunctionCallExpression:  `Math.max(1, 2)`,
	PromiseChainExpression:  `Promise.resolve(1).then(x => x + 1)`,
	ObjectLiteralExpression: `{a: 1, b: 2}`,
	AssignmentStatement:     `var x = 5`,
	ControlFlowStatement:    `if (true) { 1 } else { 2 }`,
	TopLevelReturn:          `return 42`,

	UndefinedVariable: `undefinedVar`,
	UndefinedServer:   `nonexistentServer.tool({})`,
	UndefinedTool:     `BifrostClient.nonexistentTool({})`,
	SyntaxError:       `var x = `,
	RuntimeError:      `null.someProperty`,

	NestedPromiseChains:   `Promise.resolve(1).then(x => Promise.resolve(x + 1).then(y => y + 1))`,
	PromiseErrorHandling:  `Promise.reject("error").catch(err => "handled")`,
	ComplexDataStructures: `[{a: 1}, {b: 2}].map(x => x.a || x.b)`,
	MultiLineExpression:   `BifrostClient.echo({message: "test"})\n  .then(result => {\n    return result;\n  })`,
	EmptyCode:             ``,
	WhitespaceOnly:        `   \n\t  `,
	CommentsOnly:          `// comment\n/* another */`,
	FunctionDefinition:    `function test() { return 1; }`,

	BrowserAPITest:  `typeof fetch`,
	NodeAPITest:     `typeof require`,
	AsyncAwaitTest:  `async function test() { await Promise.resolve(1); }`,
	EnvironmentTest: `__MCP_ENV__.serverKeys`,
}

// ExpectedResults contains expected results for validation
var ExpectedResults = struct {
	SimpleExpressionResult interface{}
	EchoResult             string
	AddResult              float64
	MultiplyResult         float64
}{
	SimpleExpressionResult: float64(2),
	EchoResult:             "hello",
	AddResult:              float64(3),
	MultiplyResult:         float64(6),
}
