// The searchable text a collection item is matched against, for both provider
// partitioning and feature selection.
//
// What goes in here decides which provider fork a row runs in, so it has to be
// the row's IDENTITY - what it calls and what it is named - and nothing else.
//
// Test-script source is deliberately excluded. It used to be folded in, and the
// consequence was not theoretical: every implicit cell of the cross-provider
// cache matrix carried the comment
//
//     // means the same thing it does on the anthropic shape.
//
// in its generated assertion script, which was the ONLY occurrence of
// "anthropic" in a gemini or openai cell. Those cells therefore matched the
// anthropic partition, and the `--provider anthropic` cache fork came out 85%
// foreign - 140 of 164 matrix rows belonging to openai, gemini, bedrock and
// azure, billed and reported as anthropic. Prose in a comment must never route a
// request.
//
// Pre-request/test scripts also routinely name provider-specific fields while
// asserting on them ("cached_tokens", "cachePoint"), so including them makes any
// row that merely CHECKS for a provider's shape look like that provider's row.

export const stripBase64Blobs = (s) => s.replace(/[A-Za-z0-9+/]{40,}={0,2}/g, "");

// Postman writes a description either as a bare string or as { content, type },
// and --extra-collection pushes external items in verbatim - so both encodings
// arrive here. Reading only the string form meant the same description routed a
// row or not depending on which shape its authoring tool happened to emit.
const descriptionTextOf = (description) => {
  if (typeof description === "string") return description;
  if (description && typeof description.content === "string") return description.content;
  return undefined;
};

// identityOf reduces an item to the parts that say what it is and what it calls.
// Anything omitted here cannot influence provider or feature matching.
const identityOf = (item) => ({
  name: item.name,
  request: item.request,
  // Folder-level items carry no request; their name still identifies them.
  description: descriptionTextOf(item.description),
});

export function buildHaystack(item, ancestorNames) {
  return stripBase64Blobs(
    JSON.stringify(identityOf(item)) + " " + (ancestorNames || []).join(" ")
  ).toLowerCase();
}
