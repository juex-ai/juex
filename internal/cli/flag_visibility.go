package cli

import (
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const unavailableInheritedFlagsAnnotation = "juex.ai/unavailable-inherited-flags"

func declareUnavailableInheritedFlags(cmd *cobra.Command, names ...string) {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	names = append([]string(nil), names...)
	sort.Strings(names)
	cmd.Annotations[unavailableInheritedFlagsAnnotation] = strings.Join(names, ",")

	renderHelp := cmd.HelpFunc()
	cmd.SetHelpFunc(func(helpCmd *cobra.Command, args []string) {
		restore := hideUnavailableInheritedFlags(helpCmd)
		defer restore()
		renderHelp(helpCmd, args)
	})
}

func inheritedFlagAvailable(cmd *cobra.Command, name string) bool {
	for current := cmd; current != nil; current = current.Parent() {
		for unavailable := range strings.SplitSeq(
			current.Annotations[unavailableInheritedFlagsAnnotation],
			",",
		) {
			if unavailable == name {
				return false
			}
		}
	}
	return true
}

func hideUnavailableInheritedFlags(cmd *cobra.Command) func() {
	var restore []*pflag.Flag
	cmd.InheritedFlags().VisitAll(func(flag *pflag.Flag) {
		if flag.Hidden || inheritedFlagAvailable(cmd, flag.Name) {
			return
		}
		flag.Hidden = true
		restore = append(restore, flag)
	})
	return func() {
		for _, flag := range restore {
			flag.Hidden = false
		}
	}
}
