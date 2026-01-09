package mcp

// CodeFixtures contains sample TypeScript code snippets for testing
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
	CommentsOnly          string
	FunctionDefinition    string

	// Environment tests
	AsyncAwaitTest  string
	EnvironmentTest string

	// Long code test
	LongCodeExecution string

	// Auto-execute validation tests
	CodeWithAutoExecuteTool    string
	CodeWithNonAutoExecuteTool string
	CodeWithMixedAutoExecute   string
	CodeWithMultipleClients    string
	CodeWithNoToolCalls        string
	CodeWithListToolFiles      string
	CodeWithReadToolFile       string

	// Mixed client scenarios
	CodeCallingCodeModeTool    string
	CodeCallingNonCodeModeTool string
	CodeCallingMultipleServers string
	CodeWithUndefinedServer    string
	CodeWithUndefinedTool      string

	// Agent mode scenarios
	CodeForAgentModeAutoExecute    string
	CodeForAgentModeNonAutoExecute string
}{
	SimpleExpression:     `return 1 + 1`,
	SimpleString:         `return "hello"`,
	VariableAssignment:   `var x = 5; return x`,
	ConsoleLogging:       `console.log("test"); return "logged"`,
	ExplicitReturn:       `return 42`,
	AutoReturnExpression: `return 2 + 2`, // Note: Now requires explicit return

	SingleToolCall:          `const result = await BifrostClient.echo({message: "hello"}); return result`,
	ToolCallWithPromise:     `const result = await BifrostClient.echo({message: "test"}); console.log(result); return result`,
	ToolCallChain:           `const result1 = await BifrostClient.add({a: 1, b: 2}); const result2 = await BifrostClient.multiply({a: result1, b: 3}); return result2`,
	ToolCallErrorHandling:   `try { await BifrostClient.error_tool({}); } catch (err) { console.error(err); return "handled"; }`,
	MultipleServerToolCalls: `const r1 = await BifrostClient.echo({message: "test"}); const r2 = await BifrostClient.add({a: 1, b: 2}); return r2`,
	ToolCallWithComplexArgs: `return await BifrostClient.complex_args_tool({data: {nested: {value: 42}}})`,

	ImportStatement:          `import { something } from "module"; return 1 + 1`,
	ExportStatement:          `export const x = 5; return x`,
	MultipleImportExport:     `import a from "a"; import b from "b"; export const c = 1; return 2 + 2`,
	ImportExportWithComments: `// comment\nimport x from "x";\n// another comment\nreturn 2 + 2`,

	FunctionCallExpression:  `return Math.max(1, 2)`,                      // Note: Now requires explicit return
	PromiseChainExpression:  `return Promise.resolve(1).then(x => x + 1)`, // Note: Now requires explicit return
	ObjectLiteralExpression: `return {a: 1, b: 2}`,                        // Note: Now requires explicit return
	AssignmentStatement:     `var x = 5`,                                  // Assignment statements don't return values
	ControlFlowStatement:    `if (true) { return 1; } else { return 2; }`, // Note: Now requires explicit return
	TopLevelReturn:          `return 42`,

	UndefinedVariable: `return undefinedVar`,                      // Will cause runtime error
	UndefinedServer:   `return nonexistentServer.tool({})`,        // Will cause runtime error
	UndefinedTool:     `return BifrostClient.nonexistentTool({})`, // Will cause runtime error
	SyntaxError:       `var x = `,                                 // Syntax error - no return needed
	RuntimeError:      `return null.someProperty`,                 // Will cause runtime error

	NestedPromiseChains:   `return Promise.resolve(1).then(x => Promise.resolve(x + 1).then(y => y + 1))`, // Note: Now requires explicit return
	PromiseErrorHandling:  `return Promise.reject("error").catch(err => "handled")`,                       // Note: Now requires explicit return
	ComplexDataStructures: `return [{a: 1}, {b: 2}].map(x => x.a || x.b)`,                                 // Note: Now requires explicit return
	MultiLineExpression:   `const result = await BifrostClient.echo({message: "test"});\n  return result`, // Note: Now requires explicit return
	EmptyCode:             ``,
	CommentsOnly:          `// comment\n/* another */`,
	FunctionDefinition:    `function test() { return 1; } return test()`, // Note: Now requires explicit return for function call

	AsyncAwaitTest:  `async function test() { const result = await Promise.resolve(1); return result; } return test()`,
	EnvironmentTest: `return __MCP_ENV__.serverKeys`,

	LongCodeExecution: `// Long and complex code execution test with extensive operations\n` +
		`(async function() {\n` +
		`  var results = [];\n` +
		`  var sum = 0;\n` +
		`  var processedData = [];\n` +
		`  var executionLog = [];\n` +
		`  \n` +
		`  // Initialize execution context\n` +
		`  var context = {\n` +
		`    startTime: Date.now(),\n` +
		`    steps: 0,\n` +
		`    errors: [],\n` +
		`    warnings: []\n` +
		`  };\n` +
		`  \n` +
		`  try {\n` +
		`    // Step 1: Initial echo call\n` +
		`    const result1 = await BifrostClient.echo({message: "step1"});\n` +
		`    console.log("Step 1 completed:", result1);\n` +
		`    results.push(result1);\n` +
		`    context.steps++;\n` +
		`    executionLog.push({step: 1, action: "echo", result: result1});\n` +
		`    \n` +
		`    // Step 2: Add operation\n` +
		`    const result2 = await BifrostClient.add({a: 10, b: 20});\n` +
		`    console.log("Step 2 completed:", result2);\n` +
		`    results.push(result2);\n` +
		`    sum += result2;\n` +
		`    context.steps++;\n` +
		`    executionLog.push({step: 2, action: "add", result: result2, sum: sum});\n` +
		`    \n` +
		`    // Conditional logic based on result\n` +
		`    let result3;\n` +
		`    if (result2 > 25) {\n` +
		`      console.log("Result is greater than 25, proceeding with multiplication");\n` +
		`      result3 = await BifrostClient.multiply({a: result2, b: 2});\n` +
		`    } else {\n` +
		`      console.log("Result is less than or equal to 25, using add again");\n` +
		`      result3 = await BifrostClient.add({a: result2, b: 5});\n` +
		`    }\n` +
		`    console.log("Step 3 completed:", result3);\n` +
		`    results.push(result3);\n` +
		`    sum += result3;\n` +
		`    context.steps++;\n` +
		`    executionLog.push({step: 3, action: "math", result: result3, sum: sum});\n` +
		`    \n` +
		`    // Step 4: Echo call\n` +
		`    const result4 = await BifrostClient.echo({message: "step4"});\n` +
		`    console.log("Step 4 completed:", result4);\n` +
		`    results.push(result4);\n` +
		`    context.steps++;\n` +
		`    executionLog.push({step: 4, action: "echo", result: result4});\n` +
		`    \n` +
		`    // Complex loop with nested operations\n` +
		`    for (var i = 0; i < 20; i++) {\n` +
		`      sum += i;\n` +
		`      if (i % 3 === 0) {\n` +
		`        processedData.push({\n` +
		`          index: i,\n` +
		`          value: i * 2,\n` +
		`          isMultipleOfThree: true\n` +
		`        });\n` +
		`      } else if (i % 2 === 0) {\n` +
		`        processedData.push({\n` +
		`          index: i,\n` +
		`          value: i * 1.5,\n` +
		`          isEven: true\n` +
		`        });\n` +
		`      } else {\n` +
		`        processedData.push({\n` +
		`          index: i,\n` +
		`          value: i,\n` +
		`          isOdd: true\n` +
		`        });\n` +
		`      }\n` +
		`    }\n` +
		`    \n` +
		`    console.log("Processed", processedData.length, "data items");\n` +
		`    \n` +
		`    // Step 5: Get data\n` +
		`    const result5 = await BifrostClient.get_data({key: "test"});\n` +
		`    console.log("Step 5 completed:", result5);\n` +
		`    results.push(result5);\n` +
		`    context.steps++;\n` +
		`    executionLog.push({step: 5, action: "get_data", result: result5});\n` +
		`    \n` +
		`    // Nested data processing\n` +
		`    var nestedResults = [];\n` +
		`    for (var j = 0; j < results.length; j++) {\n` +
		`      var item = results[j];\n` +
		`      nestedResults.push({\n` +
		`        original: item,\n` +
		`        processed: typeof item === "string" ? item.toUpperCase() : item * 1.1,\n` +
		`        index: j,\n` +
		`        metadata: {\n` +
		`          type: typeof item,\n` +
		`          isString: typeof item === "string",\n` +
		`          isNumber: typeof item === "number"\n` +
		`        }\n` +
		`      });\n` +
		`    }\n` +
		`    \n` +
		`    // Step 6: Final echo call\n` +
		`    const result6 = await BifrostClient.echo({message: "final_step"});\n` +
		`    console.log("Step 6 completed:", result6);\n` +
		`    results.push(result6);\n` +
		`    context.steps++;\n` +
		`    executionLog.push({step: 6, action: "echo", result: result6});\n` +
		`    \n` +
		`    // Calculate statistics\n` +
		`    var stats = {\n` +
		`      totalResults: results.length,\n` +
		`      numericSum: sum,\n` +
		`      average: sum / results.length,\n` +
		`      processedItems: processedData.length,\n` +
		`      executionSteps: context.steps\n` +
		`    };\n` +
		`    \n` +
		`    // Create comprehensive final data structure\n` +
		`    var finalData = {\n` +
		`      results: results,\n` +
		`      processedData: processedData,\n` +
		`      executionLog: executionLog,\n` +
		`      statistics: stats,\n` +
		`      context: {\n` +
		`        steps: context.steps,\n` +
		`        executionTime: Date.now() - context.startTime,\n` +
		`        errors: context.errors,\n` +
		`        warnings: context.warnings\n` +
		`      },\n` +
		`      metadata: {\n` +
		`        executed: true,\n` +
		`        completed: true,\n` +
		`        totalOperations: context.steps,\n` +
		`        dataProcessed: processedData.length,\n` +
		`        finalSum: sum,\n` +
		`        resultCount: results.length\n` +
		`      }\n` +
		`    };\n` +
		`    \n` +
		`    console.log("Final statistics:", JSON.stringify(stats));\n` +
		`    console.log("Execution completed successfully with", context.steps, "steps");\n` +
		`    console.log("Processed", processedData.length, "data items");\n` +
		`    console.log("Final sum:", sum);\n` +
		`    \n` +
		`    return finalData;\n` +
		`  } catch (error) {\n` +
		`    console.error("Error in long execution:", error);\n` +
		`    context.errors.push(error.toString());\n` +
		`    return {\n` +
		`      error: error.toString(),\n` +
		`      context: context,\n` +
		`      partialResults: results,\n` +
		`      partialSum: sum\n` +
		`    };\n` +
		`  }\n` +
		`})()`,

	// Auto-execute validation tests
	CodeWithAutoExecuteTool:    `const result = await BifrostClient.echo({message: "auto-execute"}); return result`,
	CodeWithNonAutoExecuteTool: `const result = await BifrostClient.multiply({a: 2, b: 3}); return result`,
	CodeWithMixedAutoExecute:   `const r1 = await BifrostClient.echo({message: "auto"}); const r2 = await BifrostClient.multiply({a: 2, b: 3}); return r2`,
	CodeWithMultipleClients:    `const r1 = await BifrostClient.echo({message: "test"}); const r2 = await Server2.add({a: 1, b: 2}); return r2`,
	CodeWithNoToolCalls:        `return 42`,
	CodeWithListToolFiles:      `const files = await BifrostClient.listToolFiles({}); return files`,
	CodeWithReadToolFile:       `const content = await BifrostClient.readToolFile({fileName: "BifrostClient.d.ts"}); return content`,

	// Mixed client scenarios
	CodeCallingCodeModeTool:    `const result = await BifrostClient.echo({message: "test"}); return result`,
	CodeCallingNonCodeModeTool: `const result = await NonCodeModeClient.someTool({}); return result`,
	CodeCallingMultipleServers: `const r1 = await BifrostClient.echo({message: "test"}); const r2 = await Server2.add({a: 1, b: 2}); return {r1, r2}`,
	CodeWithUndefinedServer:    `const result = await UndefinedServer.tool({}); return result`,
	CodeWithUndefinedTool:      `const result = await BifrostClient.undefinedTool({}); return result`,

	// Agent mode scenarios
	CodeForAgentModeAutoExecute:    `const result = await BifrostClient.echo({message: "agent-auto"}); return result`,
	CodeForAgentModeNonAutoExecute: `const result = await BifrostClient.multiply({a: 5, b: 6}); return result`,
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
