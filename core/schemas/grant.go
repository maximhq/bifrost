package schemas

// What a request has been granted: who it is made by, what it may reach, and what pays for it.
//
// Every request carries one Grant, reached through its context. It is an envelope of sections that
// settle at different points as the request moves through the gateway. The Identity settles first,
// when the transport authenticates whatever the request presented. The Access settles once the
// permits the caller holds have been resolved, before anything decides where the request goes, and
// holds for the whole request: a fallback changes the provider, not the caller. The Limits settle
// per attempt, once the provider and model it will use are known, and are replaced whole for the
// next attempt. Each section is replaced rather than edited in place, so anything holding an
// earlier answer keeps reading it.
//
// This file declares the shapes and nothing else. What implements them, and where permits come
// from, is decided by whoever wires the gateway; core reads the answers and drives when each
// section is settled. A request that carries no Grant, or a Grant with no Access, is one nothing
// governs: it presented no credential and nothing granted it anything, so it reaches every
// provider, model, key and tool the deployment has, and a consumer that narrows something leaves
// it alone. That is a different thing from access that permits nothing, and consumers must tell
// the two apart.

// Grant is what a request has been granted, carried on its context for the life of the request.
//
// A nil section means not settled, which every reader has to tell apart from settled-and-empty: no
// access resolved is not access that permits nothing, and no limits resolved is not an attempt that
// answers to none.
//
// Any layer may replace a section, plugins included: a deployment that governs requests its own
// way changes what a request is attributed to, may reach, or answers to through the same sections
// everything else reads. Writes report whether they landed. Recording nothing is a no-op: a section
// can be replaced by a later answer but never emptied by one, because a reader cannot tell an
// emptied section from one that was never settled.
type Grant interface {
	// Identity is who the request is made by, or nil when nothing has settled it.
	Identity() Identity
	// Access is what the request may reach, or nil when nothing has resolved it: a request that
	// carries no permit, which nothing governs, or a path that runs before resolution.
	Access() Access
	// Limits is what the current attempt answers to, or nil until its provider and model are
	// settled and its limits resolved.
	Limits() Limits

	// SetIdentity records who the request is made by.
	SetIdentity(identity Identity) bool
	// SetAccess records what the request may reach, so everything downstream reads one answer
	// rather than resolving its own.
	SetAccess(access Access) bool
	// SetLimits records what the current attempt answers to, replacing what the previous attempt
	// answered to.
	SetLimits(limits Limits) bool
}

// Identity is who a request is made by and where they sit in the organization, as resolved for
// this request.
//
// It is settled in two steps by two layers. The transport records what was presented, which is all
// it can know. Resolving the request's access then fills in what that resolves to: the key row, the
// user it belongs to, and the teams, business units and customers above them. Everything downstream
// that attributes the request, whether to a log row, a routing rule or a budget, reads it from here
// rather than looking any of it up again.
//
// Teams, Customers and BusinessUnits are every one the identity reaches, for attribution that fans
// out across an organization. Each list is ordered with the one the request is attributed to first:
// the team a key is attached to, the customer that key names directly, the user's primary team.
type Identity interface {
	// Credential is what the request is authenticated by. A request that presented a key alongside
	// an already verified identity still has one: whichever the deployment's precedence chose, with
	// the other recorded as the resolved fact it stands for (see User and VirtualKey). Its Kind is
	// empty when the request presented nothing.
	Credential() Credential
	// Presented reports whether the request presented anything at all. That is a different question
	// from whether what it presented resolved to something: a credential that resolves to nothing
	// is refused, while a request that presented nothing carries no access at all and is left to
	// the mandatory-auth check to admit or refuse.
	Presented() bool

	// User is the user the request is attributed to: the one who authenticated, or the one who
	// holds the key that was presented. Nil when no user is known.
	User() *UserRef
	// VirtualKey is the key the request was made with, once the presented value has resolved to a
	// row. Nil when no key was presented or it resolved to nothing.
	VirtualKey() *EntityRef

	// Teams, Customers and BusinessUnits are every one the identity reaches, the attributed one
	// first. Empty when unknown.
	Teams() []EntityRef
	Customers() []EntityRef
	BusinessUnits() []EntityRef

	// Project is the scope the request named and was admitted to. Nil when the request named no
	// project, or named one it may not use; the two are told apart by reading the request itself.
	Project() *EntityRef
}

// Permit is what one source confers: a named bundle of access.
//
// (Type, ID) is a permit's identity: it names the source whose access this is, and is what
// attribution refers to. Name is for display in refusals and logs; renaming a source changes Name,
// never its identity.
//
// What pays for exercising it is not here. A permit describes what may be reached; what that costs
// is gathered by whoever holds the budgets, keyed by the permit's identity, once the provider and
// model an attempt will use are known (see Access.PermitsForModel).
//
// The slices a permit hands out are its own and must be read, not modified. Permits are told apart
// by identity, so an implementation has to be comparable: a pointer, as the one that ships is.
type Permit interface {
	// Type is the kind of source the permit comes from, as the resolver that built it names kinds.
	Type() string
	ID() string
	Name() string
	IsActive() bool
	IsExpired() bool

	// ProviderPermits is every provider the permit allows, with what each allows.
	ProviderPermits() []ProviderPermit
	// MCPPermits is every MCP client the permit allows tools of.
	MCPPermits() []MCPPermit
}

// Access is an attempt's resolved access: the permits the caller holds, the permit scoping the
// request, and the mode composing the two. It is built once per attempt and never mutated, so every
// consumer of that attempt sees one answer.
//
// The caller's permits are read as one: what any of them permits, the caller may reach, any key
// any of them allows may serve it, and every one of them that permits it pays for it (see
// PermitsForModel). There is one scoping permit at most, composed under one mode.
//
// Either side may be empty. No caller permits means the caller holds no access of their own, which
// is not the same as holding a permit that permits nothing. No scoping permit means the mode is
// irrelevant and the answer is what the caller's permits permit.
type Access interface {
	// Bases returns the permits the caller holds, in attribution order. Empty when they hold none.
	Bases() []Permit
	// Scoping returns the permit scoping this request, or nil when nothing scopes it.
	Scoping() Permit
	// Mode returns the composition mode governing this request, as the resolver that built it names
	// modes. It is meaningless when nothing scopes the request.
	Mode() string
	// IsScoped reports whether a scoping permit applies to this request.
	IsScoped() bool

	// IsProviderAllowed reports whether the request may use provider at all.
	IsProviderAllowed(provider string) bool
	// IsModelAllowed reports whether the request may use model on provider. Blacklisted models
	// lose to nothing: a permit that blacklists the model does not permit it, whatever its
	// allowed-models list says. An empty model asks about the provider alone.
	IsModelAllowed(provider string, model string) bool
	// IsMCPToolAllowed reports whether the request may execute toolPattern, which is either
	// "<client>-<tool>" or the "<client>-*" wildcard standing for every tool of a client. A
	// wildcard is permitted when the client is granted any tool at all; narrowing a wildcard down
	// to the tools actually granted is MCPToolIncludeList's job.
	IsMCPToolAllowed(toolPattern string) bool

	// PermitsForModel returns the permits the request answers to for model on provider: every one
	// of the caller's permits that permits the pair, then the scoping permit whenever the request
	// is scoped. Nil when the request may not use the pair at all. An empty model asks about the
	// provider alone.
	//
	// This is the attribution rule, and what the pair costs is gathered against every permit named
	// here. The caller's permits are read as one, so each of them that permits the pair covers the
	// request and each is charged; there is no affording something because a different budget has
	// room. The scoping permit is named whether or not it was the one that admitted the pair,
	// because the request happens inside the scope and is that scope's spend.
	PermitsForModel(provider string, model string) []Permit

	// KeysForModel returns the provider keys the request may use for model on provider, and whether
	// that list is restrictive at all: any key any of the caller's permitting permits allows,
	// composed with the scoping permit's under the mode. restricted is false when every key of the
	// provider is allowed, in which case keyIDs is nil; when restricted is true, only keyIDs may be
	// used and an empty list allows none.
	KeysForModel(provider string, model string) (keyIDs []string, restricted bool)
	// ProvidersForModel returns every provider permit the request may serve model from, in
	// evaluation order: the caller's permits first, each in full and in their order, then providers
	// gained purely through the scoping permit. Weight is passed through untouched: filtering
	// unweighted candidates, and applying budget and rate-limit exclusions, is the caller's decision.
	ProvidersForModel(model string) []ProviderCandidate
	// GrantedProvidersForModel returns the providers that permit model, for consumers that gate on the
	// provider rather than on the exact model: routing layers and model listings. Model names are
	// matched by name here, since those consumers sit on top of the model resolution their own
	// layers already run. An empty model means "no model to filter on", which keeps every granted
	// provider.
	GrantedProvidersForModel(model string) []string

	// MCPToolIncludeList returns the tool patterns the request may execute, as "<client>-<tool>"
	// entries and "<client>-*" wildcards for clients granted every tool. An empty result means no
	// tool may be executed.
	MCPToolIncludeList() []string
	// NarrowMCPToolIncludeList narrows a tool list the caller asked for down to what this access
	// permits, so a request-supplied include list can only ever narrow the access, never widen it.
	// The result is never nil: an empty list permits no tool, which downstream has to tell apart
	// from no list having been supplied at all. A request with no access is not narrowed at all,
	// which is the caller's to notice before asking.
	NarrowMCPToolIncludeList(requested []string) []string

	// DeniedPermitsForModel returns the permits that refused model on provider, so the refusal can name them.
	// An empty model asks about the provider alone. Nil when the request is allowed.
	DeniedPermitsForModel(provider string, model string) []Permit
	// DeniedPermitsForMCPTool is DeniedPermitsForModel for an MCP tool pattern.
	DeniedPermitsForMCPTool(toolPattern string) []Permit
}

// Limits is what an attempt answers to once its provider and model are settled: every budget and
// every rate limit, whoever holds them, already selected for that pair. What is checked, what is
// named as a co-payer on the log row, and what is charged all read the same list, so they cannot
// disagree about which limits an attempt was subject to. Either list is nil when nothing of that
// kind applies.
type Limits interface {
	Budgets() []Limit
	RateLimits() []Limit
}

// ProviderPermit is permission to use one provider, with the model and key restrictions that come
// with it. It mirrors the semantics of a virtual key's provider config, expressed independently of
// any particular source.
//
// The lists are the same types the configuration lists are, and carry their semantics with them: a
// list holding only "*" is unrestricted, an empty list holds nothing. Key IDs are identifiers
// rather than names, so whoever selects a key matches them exactly; the case-folding membership
// methods are for the model and tool lists.
type ProviderPermit struct {
	Provider          string    // provider name, as configured
	AllowedModels     WhiteList // ["*"] allows all models; empty allows none (deny by default)
	BlacklistedModels BlackList // blocked models; wins over AllowedModels
	KeyIDs            WhiteList // ["*"] allows all keys of the provider; empty allows none.
	//                             Composes with the other side's list, but only where that side also
	//                             authorizes the request for the provider: a permit the request is not
	//                             proceeding on does not get to say which keys serve it.
	Weight *float64 // load-balancing weight; nil means the provider is not a load-balancing
	//                  candidate. Unlike the permissions, this does not compose: there is no
	//                  meaningful intersection of two preferences, so a scoping permit that
	//                  expresses one wins as the more specific context.
}

// MCPPermit is permission to execute tools of one MCP client.
type MCPPermit struct {
	Client string // stable client identifier; identifies the client across renames and
	//                decides which permit applies to a client
	ClientName string    // client name, as used in "<client>-<tool>" tool patterns
	Tools      WhiteList // ["*"] allows every tool of the client; empty allows none. Matched
	//                       against the tool's own name, after the "<client>-" prefix is trimmed.
}

// ProviderCandidate is one way a request could be served: a provider, with the weight and keys it
// would operate under. One per provider permit, so a request holding two for the same provider has
// two candidates that may carry different weights and keys.
//
// Which permit offered the candidate is not restated here: a candidate is offered only by a permit
// that permits the model on its provider, and every such permit pays for the pair, so what the
// candidate costs is gathered against Access.PermitsForModel, given its provider and the model.
type ProviderCandidate struct {
	Provider string
	Weight   *float64  // nil means the candidate has no weight assigned
	KeyIDs   WhiteList // ["*"] allows all keys of the provider; per candidate, so two permits for
	//                    one provider can carry different keys. Consumers stamping a key restriction
	//                    for the request use Access.KeysForModel instead: it answers for the request,
	//                    not per candidate.
}

// Limit is one budget or one rate limit a request answers to: what to load when enforcing it, and
// whose it is.
//
// ID is an identifier, never the record. A budget's usage is live and replaced in place as
// requests spend, so a copy taken when the permit was built would answer from a balance that has
// since moved; whoever enforces a limit loads it by ID at the moment it enforces.
//
// HolderKind, HolderID and HolderName say where the limit came from, because a refusal has to be
// able to name what refused. "The key's monthly budget" and "your team's" are different answers to
// the user, and a bare identifier cannot tell them apart. HolderID also keeps two limits of the
// same kind distinct, which matters when one holder carries several.
//
// Which requests a limit applies to is not recorded on it. It is settled by where the limit comes
// from and by when it is gathered, since a request can fail over to another provider or have its
// model re-resolved by a routing rule. So a limit that has been gathered for an attempt is one to
// enforce; there is nothing further to match it against.
type Limit struct {
	ID string

	HolderKind string
	HolderID   string
	HolderName string

	// What the limit was gathered for, when it is scoped to a provider or a model. Empty when it is
	// scoped to neither: an empty name is never a real provider or model, so nothing else is needed
	// to say "unscoped". Descriptive only, nothing selects on them, so that a limit which is
	// recorded on a log row or named in a refusal can say what it was scoped to.
	Provider string
	Model    string
}

// Credential is what the request presented to say who it is. Kind names what it was, in the
// vocabulary of whoever authenticates requests; an empty Kind means nothing was presented.
//
// Value is whatever identifies the credential to the layer that resolves it: the key itself for a
// virtual key, which is looked up by value, and an identifier for kinds that were verified when they
// were accepted, such as an API key's id or a session's subject. It is not a display value and must
// not be logged as one.
type Credential struct {
	Kind  string
	Value string
}

// EntityRef names something in the organization by identifier and display name. The identifier is
// the identity; the name is for logs and refusals, and renaming the entity changes only that.
type EntityRef struct {
	ID   string
	Name string
}

// UserRef is the person a request is attributed to.
type UserRef struct {
	ID    string
	Name  string
	Email string
}
