package parser

import (
	"testing"

	"github.com/provasign/grove/internal/core"
	"github.com/provasign/grove/internal/graph"
)

// End-to-end through the REAL parser (no hand-built CallSites): the TypeORM
// shape that motivated multi-hop receiver resolution. change_impact on an
// interface method must include callers that reach it via a two-hop
// interface-typed field chain (`this.connection.driver.escape(...)`), must
// resolve the family nominally (implements clauses), and must NOT admit a
// structural impostor (same-named method, no implements clause).
func TestTsTwoHopFieldChainEndToEnd(t *testing.T) {
	files := map[string]string{
		"src/driver/Driver.ts": `
export interface Driver {
    options: object
    escape(name: string): string
    normalize(v: string): string
}`,
		"src/driver/MysqlDriver.ts": `
import { Driver } from "./Driver"
export class MysqlDriver implements Driver {
    options: object = {}
    escape(name: string): string { return "[" + name + "]" }
    normalize(v: string): string { return v }
}`,
		"src/driver/PostgresDriver.ts": `
import { Driver } from "./Driver"
export class PostgresDriver implements Driver {
    options: object = {}
    escape(name: string): string { return '"' + name + '"' }
    normalize(v: string): string { return v }
}`,
		"src/DataSource.ts": `
import { Driver } from "./driver/Driver"
export class DataSource {
    readonly name: string = "default"
    driver: Driver
    isInitialized: boolean = false
}`,
		"src/EntityManager.ts": `
import { DataSource } from "./DataSource"
export class EntityManager {
    readonly connection: DataSource
    increment(column: string): string {
        return this.connection.driver.escape(column) + " + 1"
    }
}`,
		// Impostor: has escape() but neither implements Driver nor its full
		// member set — must stay out of the family.
		"src/QueryShim.ts": `
export class QueryShim {
    escape(name: string): string { return name }
}`,
		// Three-hop chain with an INTERFACE-typed middle hop (the TypeORM
		// tree-executor pattern): this.queryRunner.connection.driver.escape.
		"src/QueryRunner.ts": `
import { DataSource } from "./DataSource"
export interface QueryRunner {
    readonly connection: DataSource
    release(): Promise<void>
}`,
		"src/TreeExecutor.ts": `
import { QueryRunner } from "./QueryRunner"
export class TreeExecutor {
    protected queryRunner: QueryRunner
    insert(column: string): string {
        const esc = (alias: string) => this.queryRunner.connection.driver.escape(alias)
        return esc(column)
    }
}`,
	}
	g := graph.New()
	var all []core.SymbolRecord
	for fp, src := range files {
		syms, ok, _ := extractSymbolsFromAST("typescript", fp, "sha", []byte(src), nil)
		if !ok {
			t.Fatalf("parse failed for %s", fp)
		}
		all = append(all, syms...)
	}
	g.Replace(all, len(files))

	r, err := g.ChangeImpact("Driver.escape")
	if err != nil {
		t.Fatalf("ChangeImpact: %v", err)
	}
	fam := map[string]bool{}
	for _, s := range r.Family {
		fam[s.QualifiedName] = true
	}
	if !fam["MysqlDriver.escape"] || !fam["PostgresDriver.escape"] {
		t.Errorf("nominal family incomplete: %v", fam)
	}
	if fam["QueryShim.escape"] {
		t.Errorf("structural impostor QueryShim.escape admitted to family: %v", fam)
	}
	callers := map[string]bool{}
	var got []string
	for _, c := range r.Callers {
		callers[c.QualifiedName] = true
		got = append(got, c.QualifiedName)
	}
	if !callers["EntityManager.increment"] {
		t.Errorf("two-hop caller EntityManager.increment missing; callers=%v", got)
	}
	if !callers["TreeExecutor.insert"] {
		t.Errorf("three-hop (interface middle hop) caller TreeExecutor.insert missing; callers=%v", got)
	}
	// The interface's own member is not an indexed symbol, but its
	// declaration site must still be in the change-set (synthesized).
	foundDecl := false
	for _, d := range r.Declarations {
		if d.FilePath == "src/driver/Driver.ts" && d.Name == "escape" {
			foundDecl = true
		}
	}
	if !foundDecl {
		t.Errorf("synthesized interface declaration missing; decls=%d", len(r.Declarations))
	}
}
