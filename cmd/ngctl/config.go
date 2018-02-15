package main

import (
	"context"
	"fmt"
	"github.com/coreos/etcd/client"
	"github.com/fatih/color"
	log "github.com/inconshreveable/log15"
	"github.com/spf13/cobra"
	"os"
	"strings"
)

func init() {
	commonCmd := &cobra.Command{
		Use: "common",
		Run: func(cmd *cobra.Command, args []string) {
			cmd.Help()
		},
	}
	commonCmd.AddCommand(&cobra.Command{
		Use: "dump",
		Run: func(cmd *cobra.Command, args []string) {
			etcdKeys := getEtcdKeys()
			fmt.Print(getKey(etcdKeys, "/config/common"))
		},
	})
	commonCmd.AddCommand(&cobra.Command{
		Use: "edit",
		Run: func(cmd *cobra.Command, args []string) {
			etcdKeys := getEtcdKeys()
			editKey(etcdKeys, "/config/common")
		},
	})

	coinserverCmd := &cobra.Command{
		Use: "coinserver",
		Run: func(cmd *cobra.Command, args []string) {
			cmd.Help()
		},
	}
	setupConfigCommands(coinserverCmd, "coinserver")

	stratumCmd := &cobra.Command{
		Use: "stratum",
		Run: func(cmd *cobra.Command, args []string) {
			cmd.Help()
		},
	}
	setupConfigCommands(stratumCmd, "stratum")

	RootCmd.AddCommand(commonCmd)
	RootCmd.AddCommand(stratumCmd)
	RootCmd.AddCommand(coinserverCmd)
}

func setupConfigCommands(cmd *cobra.Command, serviceType string) {
	var lsCmd = &cobra.Command{
		Use:   "ls",
		Short: "Lists all service configs",
		Run: func(cmd *cobra.Command, args []string) {
			log.Info(serviceType)
			etcdKeys := getEtcdKeys()
			getOpt := &client.GetOptions{
				Recursive: true,
			}
			res, err := etcdKeys.Get(context.Background(), "/config/"+serviceType, getOpt)
			if err != nil {
				log.Crit("Unable to contact etcd", "err", err)
				os.Exit(1)
			}
			for _, node := range res.Node.Nodes {
				lbi := strings.LastIndexByte(node.Key, '/') + 1
				serviceID := node.Key[lbi:]
				color.Green("export SERVICEID=%s", serviceID)
				fmt.Println(node.Value)
				fmt.Println()
			}
		}}

	var newCmd = &cobra.Command{
		Use:   "new [name]",
		Short: "Creates a new service configuration",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			name := args[0]
			keyPath := "/config/" + serviceType + "/" + name

			etcdKeys := getEtcdKeys()
			def := getDefaultConfig(serviceType)
			newConfig, save := modifyLoop(def, keyPath)
			if !save {
				return
			}
			_, err := etcdKeys.Set(
				context.Background(), keyPath, newConfig, nil)
			if err != nil {
				log.Crit("Failed pushing config", "err", err)
				os.Exit(1)
			}
			log.Info("Successfully pushed config", "keypath", keyPath)
		}}

	var editCmd = &cobra.Command{
		Use:   "edit [name]",
		Short: "Opens the config in an editor",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			etcdKeys := getEtcdKeys()
			name := args[0]
			configKeyPath := "/config/" + serviceType + "/" + name

			editKey(etcdKeys, configKeyPath)
		}}

	var cloneCmd = &cobra.Command{
		Use:   "clone [source] [new]",
		Short: "Creates a new service config from previous",
		Args:  cobra.ExactArgs(2),
		Run: func(cmd *cobra.Command, args []string) {
			etcdKeys := getEtcdKeys()
			configKeyPath := "/config/" + serviceType + "/" + args[0]
			values := getKey(etcdKeys, configKeyPath)

			keyPath := "/config/" + serviceType + "/" + args[1]
			newConfig, save := modifyLoop(values, keyPath)
			if !save {
				return
			}
			_, err := etcdKeys.Set(
				context.Background(), keyPath, newConfig, nil)
			if err != nil {
				log.Crit("Failed pushing config", "err", err)
				os.Exit(1)
			}
			log.Info("Successfully pushed config", "keypath", keyPath)
		}}

	var rmCmd = &cobra.Command{
		Use:   "rm [name]",
		Short: "Remove a service configuration",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			etcdKeys := getEtcdKeys()
			name := args[0]
			configKeyPath := "/config/" + serviceType + "/" + name
			rmKey(etcdKeys, configKeyPath)
		}}

	var mvCmd = &cobra.Command{
		Use:   "mv [name] [new_name]",
		Short: "Change service name of a configuration",
		Args:  cobra.ExactArgs(2),
		Run: func(cmd *cobra.Command, args []string) {
			etcdKeys := getEtcdKeys()
			name := args[0]
			newName := args[1]
			configKeyPath := "/config/" + serviceType + "/" + name
			newConfigKeyPath := "/config/" + serviceType + "/" + newName
			values := getKey(etcdKeys, configKeyPath)
			writeKey(etcdKeys, newConfigKeyPath, values)
			rmKey(etcdKeys, configKeyPath)
		}}

	cmd.AddCommand(newCmd, rmCmd, lsCmd, mvCmd, editCmd, cloneCmd)
}
