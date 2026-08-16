# Responses API image-generation compatibility

The `/v1/responses` parser now accepts OpenAI-compatible hosted image-generation responses whose `action` is the scalar value `"generate"`, while retaining support for object-shaped actions. This keeps unary and streaming responses decodable before they are returned to the caller.

The schema exposes named compile-time request values (`generate`, `edit`, and `auto`) on `ResponsesToolImageGeneration`. The shared action union decodes scalar and object image-generation actions without coercing unknown values to computer actions. Known object actions retain unmodeled provider extension fields during a decode/encode round trip, and unknown scalar/object actions are preserved as raw JSON. Existing Responses handlers and stream event constants remain unchanged.

Regression coverage includes request round trips, scalar/object/unknown action fixtures, completed unary image calls, and streaming `in_progress`, `generating`, `partial_image`, and `completed` events. The live MicroK8s harness uses `gpt-5.6-luna` or another available non-Sol model for both stream modes and distinguishes schema failures from upstream capability errors.
