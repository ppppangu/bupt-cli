package cli

import (
	"context"
	"slices"
	"strings"
	"time"
	"unicode"

	"bupt.local/cli/internal/domain"
	"github.com/spf13/cobra"
)

type completionObject struct {
	id, name string
}

func (a *app) cachedCompletion(cmd *cobra.Command, specs, idOnly bool, prefix string) ([]string, cobra.ShellCompDirective) {
	directive := cobra.ShellCompDirectiveNoFileComp
	local := *a
	if err := local.loadConfig(cmd); err != nil {
		return nil, directive
	}
	b, err := local.getBackend()
	if err != nil {
		return nil, directive
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 350*time.Millisecond)
	defer cancel()

	var objects []completionObject
	if specs {
		var catalog domain.ResourceOverview
		if err := b.Call(ctx, "resources.overview", map[string]bool{"cached": true}, &catalog); err != nil {
			return nil, directive
		}
		for _, pool := range catalog.Pools {
			for _, spec := range pool.Templates {
				objects = append(objects, completionObject{id: string(spec.ID), name: spec.Name})
			}
		}
	} else {
		var instances []domain.Instance
		if err := b.Call(ctx, "instance.list", map[string]bool{"cached": true}, &instances); err != nil {
			return nil, directive
		}
		for _, instance := range instances {
			objects = append(objects, completionObject{id: string(instance.ID), name: instance.Name})
		}
	}
	counts := map[string]int{}
	for _, item := range objects {
		counts[item.name]++
	}
	safe := func(value string) bool {
		return value != "" && len(value) <= 256 && !strings.HasPrefix(value, "-") && strings.IndexFunc(value, func(r rune) bool { return !unicode.IsPrint(r) }) < 0
	}
	var matches []string
	add := func(value, description string) {
		if safe(value) && strings.HasPrefix(value, prefix) {
			if safe(description) && description != value {
				value += "\t" + description
			}
			matches = append(matches, value)
		}
	}
	for _, item := range objects {
		add(item.id, item.name)
		if !idOnly && counts[item.name] == 1 {
			add(item.name, item.id)
		}
	}
	slices.Sort(matches)
	return slices.Compact(matches), directive
}

func (a *app) installObjectCompletion(root *cobra.Command) {
	var visit func(*cobra.Command)
	visit = func(cmd *cobra.Command) {
		const instancePrefix = "bupt compute instance "
		path := cmd.CommandPath()
		switch {
		case path == "bupt compute catalog show":
			cmd.ValidArgsFunction = a.completeObject(true, false, 0)
		case strings.HasPrefix(path, instancePrefix) && cmd.Name() != "ls" && cmd.Name() != "create",
			path == "bupt ssh", path == "bupt exec", path == "bupt forward",
			path == "bupt console", path == "bupt sftp":
			cmd.ValidArgsFunction = a.completeObject(false, false, 0)
		}
		for _, flag := range []string{"spec", "spec-id", "instance"} {
			if cmd.Flags().Lookup(flag) != nil {
				_ = cmd.RegisterFlagCompletionFunc(flag, a.completeObject(flag != "instance", flag == "spec-id", -1))
			}
		}
		for _, child := range cmd.Commands() {
			visit(child)
		}
	}
	visit(root)
}

func (a *app) completeObject(specs, idOnly bool, pos int) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
		if pos >= 0 && len(args) != pos || cmd.Flags().Changed("operation") {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return a.cachedCompletion(cmd, specs, idOnly, prefix)
	}
}

// Cobra's fallback on Bash 3 assumes bash-completion is installed. Use Bash's
// native variables on a fresh macOS shell, leaving its regular helper in place.
const bashBuiltinCompletion = `
if ! declare -F _get_comp_words_by_ref >/dev/null; then
    __bupt_init_completion()
    {
        COMPREPLY=()
        words=("${COMP_WORDS[@]}")
        cword="$COMP_CWORD"
        cur="${words[cword]-}"
        if ((cword > 0)); then
            prev="${words[cword-1]}"
        fi
    }
fi
`
