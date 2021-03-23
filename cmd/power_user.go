package cmd

import (
	"fmt"

	"github.com/anchore/syft/internal"

	"github.com/anchore/syft/internal/bus"
	"github.com/anchore/syft/internal/presenter/poweruser"
	"github.com/anchore/syft/internal/ui"
	"github.com/anchore/syft/syft/event"
	"github.com/anchore/syft/syft/source"
	"github.com/pkg/profile"
	"github.com/spf13/cobra"
	"github.com/wagoodman/go-partybus"
)

const powerUserExample = `  {{.appName}} {{.command}} <image>

  Only image sources are supported (e.g. docker: , docker-archive: , oci: , etc.), the directory source (dir:) is not supported.

  All behavior is controlled via application configuration and environment variables (see https://github.com/anchore/syft#configuration)
`

var powerUserOpts = struct {
	configPath string
}{}

var powerUserCmd = &cobra.Command{
	Use:   "power-user [IMAGE]",
	Short: "Run bulk operations on container images",
	Example: internal.Tprintf(powerUserExample, map[string]interface{}{
		"appName": internal.ApplicationName,
		"command": "power-user",
	}),
	Args:          cobra.ExactArgs(1),
	Hidden:        true,
	SilenceUsage:  true,
	SilenceErrors: true,
	PreRunE: func(cmd *cobra.Command, args []string) error {
		if appConfig.Dev.ProfileCPU && appConfig.Dev.ProfileMem {
			return fmt.Errorf("cannot profile CPU and memory simultaneously")
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		if appConfig.Dev.ProfileCPU {
			defer profile.Start(profile.CPUProfile).Stop()
		} else if appConfig.Dev.ProfileMem {
			defer profile.Start(profile.MemProfile).Stop()
		}

		return powerUserExec(cmd, args)
	},
	ValidArgsFunction: dockerImageValidArgsFunction,
}

func init() {
	powerUserCmd.Flags().StringVarP(&powerUserOpts.configPath, "config", "c", "", "config file path with all power-user options")

	rootCmd.AddCommand(powerUserCmd)
}

func powerUserExec(_ *cobra.Command, args []string) error {
	errs := powerUserExecWorker(args[0])
	ux := ui.Select(appConfig.CliOptions.Verbosity > 0, appConfig.Quiet)
	return ux(errs, eventSubscription)
}

func powerUserExecWorker(userInput string) <-chan error {
	errs := make(chan error)
	go func() {
		defer close(errs)

		tasks, err := powerUserTasks()
		if err != nil {
			errs <- err
			return
		}

		checkForApplicationUpdate()

		src, cleanup, err := source.New(userInput)
		if err != nil {
			errs <- err
			return
		}
		defer cleanup()

		if src.Metadata.Scheme != source.ImageScheme {
			errs <- fmt.Errorf("the power-user subcommand only allows for 'image' schemes, given %q", src.Metadata.Scheme)
			return
		}

		analysisResults := poweruser.JSONDocumentConfig{
			SourceMetadata:    src.Metadata,
			ApplicationConfig: *appConfig,
		}

		for _, task := range tasks {
			if err = task(&analysisResults, src); err != nil {
				errs <- err
				return
			}
		}

		bus.Publish(partybus.Event{
			Type:  event.PresenterReady,
			Value: poweruser.NewJSONPresenter(analysisResults),
		})
	}()
	return errs
}
