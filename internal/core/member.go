package core

// MemberOccurrence is one syntactic occurrence of a data member's name —
// a field, property, class constant, or module/package variable — found by
// parsing source. It carries the SHAPE of the occurrence (how the name is
// reached) but no resolution: deciding whether it binds to a particular
// declaration is the graph's job (receiver typing, owner hierarchy).
type MemberOccurrence struct {
	File string `json:"file"` // repo-relative, slash-separated
	Line int    `json:"line"` // 1-based
	Col  int    `json:"col"`  // 0-based byte column of the name on the line
	// Form is how the name appears:
	//   "access"    — through a receiver or qualifier: x.f, p->f, $this->f,
	//                 Type::K, self::$s, pkg.Var
	//   "key"       — an initializer key naming the member: Go/Rust struct
	//                 literal keys, C designated initializers, object-literal
	//                 keys, C# object initializers, keyword arguments
	//   "bare"      — a plain identifier (implicit this, same-scope variable)
	//   "decl"      — a member/variable declaration of the name
	//   "localdecl" — a local variable/parameter declaration of the name
	//                 (shadows a same-named outer member in its scope)
	//   "import"    — an import of the name (TS/JS import specifier,
	//                 Python from-import); Receiver is the module path
	Form string `json:"form"`
	// Receiver is the operand/qualifier source text for "access" (with
	// whitespace removed), or the literal/called type for "key" when the
	// syntax names one. Empty when the syntax gives none.
	Receiver string `json:"receiver,omitempty"`
	Write    bool   `json:"write,omitempty"`   // assignment target / ++ / --
	Call     bool   `json:"call,omitempty"`    // the access is the callee of a call
	Text     string `json:"text,omitempty"`    // the full source line, for edits
	Language string `json:"language"`          // the file's language
	Package  string `json:"package,omitempty"` // Go: the file's package clause name
}
