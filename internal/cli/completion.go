package cli

import (
	"github.com/spf13/cobra"
)

var completionCmd = &cobra.Command{
	Use:   "completion [bash|zsh|fish|powershell]",
	Short: "Generate shell completion scripts",
	Long: `Generate shell completion scripts for jai.

To load completions:

Bash:
  $ source <(jai completion bash)
  # To load completions for each session, execute once:
  # Linux:
  $ jai completion bash > /etc/bash_completion.d/jai
  # macOS:
  $ jai completion bash > $(brew --prefix)/etc/bash_completion.d/jai

Zsh:
  # If shell completion is not already enabled in your environment,
  # you will need to enable it. You can execute the following once:
  $ echo "autoload -U compinit; compinit" >> ~/.zshrc
  # To load completions for each session, execute once:
  $ jai completion zsh > "${fpath[1]}/_jai"
  # You will need to start a new shell for this setup to take effect.

Fish:
  $ jai completion fish | source
  # To load completions for each session, execute once:
  $ jai completion fish > ~/.config/fish/completions/jai.fish

PowerShell:
  PS> jai completion powershell | Out-String | Invoke-Expression
  # To load completions for every new session, add the output to your profile.
`,
	DisableFlagsInUseLine: true,
	ValidArgs:             []string{"bash", "zsh", "fish", "powershell"},
	Args:                  cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
	RunE: func(cmd *cobra.Command, args []string) error {
		out := cmd.OutOrStdout()
		switch args[0] {
		case "bash":
			return cmd.Root().GenBashCompletion(out)
		case "zsh":
			return cmd.Root().GenZshCompletion(out)
		case "fish":
			return cmd.Root().GenFishCompletion(out, true)
		case "powershell":
			return cmd.Root().GenPowerShellCompletionWithDesc(out)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(completionCmd)
}
