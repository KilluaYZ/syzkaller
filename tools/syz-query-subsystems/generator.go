// Copyright 2023 syzkaller project authors. All rights reserved.
// Use of this source code is governed by Apache 2 LICENSE that can be found in the LICENSE file.

package main

import (
	"bytes"
	"fmt"
	"go/format"
	"regexp"
	"sort"
	"strings"
	"text/template"
	"unicode"

	"github.com/google/syzkaller/pkg/serializer"
	"github.com/google/syzkaller/pkg/subsystem"
	"github.com/google/syzkaller/pkg/vcs"
)

func generateSubsystemsFile(name string, list []*subsystem.Subsystem, commit *vcs.Commit) ([]byte, error) {
	// Set names first -- we'll need them for filling in the Parents array.
	objToName := map[*subsystem.Subsystem]string{}
	for _, entry := range list {
		varName := getVarName(entry)
		if varName == "" {
			return nil, fmt.Errorf("failed to get a var name for %#v", entry.Name)
		}
		objToName[entry] = varName
	}

	// Prepare the template data.
	vars := &templateVars{
		Name:       name,
		CommitInfo: fmt.Sprintf(`Commit %s, "%.32s"`, commit.Hash, commit.Title),
		Version:    commit.Date.Year()*10000 + int(commit.Date.Month())*100 + commit.Date.Day(),
		Hierarchy:  hierarchyList(list),
	}
	for _, entry := range list {
		varName := objToName[entry]
		// The serializer does not understand parent references and just prints all the
		// nested structures.
		// Therefore we call it separately for the fields it can understand.
		parents := []string{}
		for _, p := range entry.Parents {
			parents = append(parents, objToName[p])
		}
		sort.Strings(parents)
		subsystem := &templateSubsystem{
			VarName:      varName,
			Name:         serializer.WriteString(entry.Name),
			PathRules:    serializer.WriteString(entry.PathRules),
			Parents:      parents,
			NoReminders:  entry.NoReminders,
			NoIndirectCc: entry.NoIndirectCc,
		}
		// Some of the records are mostly empty.
		if len(entry.Maintainers) > 0 {
			sort.Strings(entry.Maintainers)
			subsystem.Maintainers = serializer.WriteString(entry.Maintainers)
		}
		if len(entry.Syscalls) > 0 {
			subsystem.Syscalls = serializer.WriteString(entry.Syscalls)
		}
		if len(entry.Lists) > 0 {
			subsystem.Lists = serializer.WriteString(entry.Lists)
		}
		vars.List = append(vars.List, subsystem)
	}
	tmpl, err := template.New("source").Parse(fileTemplate)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	if err = tmpl.Execute(&b, vars); err != nil {
		return nil, err
	}
	return format.Source(b.Bytes())
}

func getVarName(entry *subsystem.Subsystem) string {
	varName := makeVarRegexp.ReplaceAllString(strings.ToLower(entry.Name), "")
	if varName == "" {
		return ""
	}
	if unicode.IsDigit(rune(varName[0])) {
		return "_" + varName
	}
	return varName
}

func hierarchyList(list []*subsystem.Subsystem) []string {
	children := map[*subsystem.Subsystem][]*subsystem.Subsystem{}
	for _, entry := range list {
		for _, p := range entry.Parents {
			children[p] = append(children[p], entry)
		}
	}
	ret := []string{}
	var dfs func(*subsystem.Subsystem, string)
	dfs = func(entry *subsystem.Subsystem, prefix string) {
		ret = append(ret, fmt.Sprintf("%s- %s", prefix, entry.Name))
		for _, child := range children[entry] {
			dfs(child, prefix+"  ")
		}
	}
	for _, entity := range list {
		if len(entity.Parents) == 0 {
			dfs(entity, "")
		}
	}
	return ret
}

var makeVarRegexp = regexp.MustCompile(`[^\w]|^([^a-z0-9]+)`)

type templateSubsystem struct {
	VarName      string
	Name         string
	Syscalls     string
	PathRules    string
	Lists        string
	Maintainers  string
	Parents      []string
	NoReminders  bool
	NoIndirectCc bool
}

type templateVars struct {
	Name       string
	Version    int
	CommitInfo string
	List       []*templateSubsystem
	Hierarchy  []string
}

const fileTemplate = `// Code generated by the syz-query-subsystems tool. DO NOT EDIT.
{{- if .CommitInfo}}
// {{.CommitInfo}}
{{- end}}

package lists

import . "github.com/google/syzkaller/pkg/subsystem"

func init() {
  RegisterList("{{.Name}}", subsystems_{{.Name}}(), {{.Version}})
}

// The subsystem list:
{{- range .Hierarchy}}
// {{.}}
{{- end}}

func subsystems_{{.Name}}() []*Subsystem{
var {{range $i, $item := .List}}
{{- if gt $i 0}}, {{end}}
{{- .VarName}}
{{- end}} Subsystem

{{range .List}}
{{.VarName}} = Subsystem{
 Name: {{.Name}},
{{- if .Syscalls}}
 Syscalls: {{.Syscalls}},
{{- end}}
{{- if .Lists}}
 Lists: {{.Lists}},
{{- end}}
{{- if .Maintainers}}
 Maintainers: {{.Maintainers}},
{{- end}}
{{- if .Parents}}
 Parents: []*Subsystem{ {{range .Parents}} &{{.}}, {{end}} },
{{- end}}
 PathRules: {{.PathRules}},
{{- if .NoReminders}}
 NoReminders: true,
{{- end}}
{{- if .NoIndirectCc}}
NoIndirectCc: true,
{{- end}}
}
{{end}}

return  []*Subsystem{
{{range .List}} &{{.VarName}}, {{- end}}
}

}
`
