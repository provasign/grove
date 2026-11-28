package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

// A framework calls an annotated handler; no call edge reaches it. Spring's
// package-private @GetMapping, @EventListener and @Scheduled methods were all
// reported as dead (MKT-04, 2026-11-28), so an agent cleaning up dead code
// would delete live endpoints. The private helper next to them is dead.
func TestDeadCodeFrameworkEntryPointsAreRoots(t *testing.T) {
	g := New()
	sym := func(name string, ann ...string) core.SymbolRecord {
		return core.SymbolRecord{ID: "src/main/java/app/C.java::C." + name, FilePath: "src/main/java/app/C.java", Language: "java",
			Kind: core.KindMethod, Name: name, QualifiedName: "C." + name, ParentSymbol: "C", RawText: "void " + name + "() {}", Annotations: ann}
	}
	g.Replace([]core.SymbolRecord{
		sym("list", `GetMapping("/orders")`),
		sym("onStart", "EventListener"),
		sym("tick", "Scheduled(fixedRate = 1000)"),
		sym("consume", `org.springframework.kafka.annotation.KafkaListener(topics = "t")`),
		sym("get", "GET"),
		sym("create", "JsonCreator"),
		sym("helper"),
		sym("audited", "Deprecated"),
	}, 1)
	dead := names(g.DeadCode(nil).Dead)
	for _, live := range []string{"list", "onStart", "tick", "consume", "get", "create"} {
		if dead[live] {
			t.Errorf("framework entry point %s reported dead", live)
		}
	}
	for _, d := range []string{"helper", "audited"} {
		if !dead[d] {
			t.Errorf("%s should still be dead: %v", d, dead)
		}
	}
}
