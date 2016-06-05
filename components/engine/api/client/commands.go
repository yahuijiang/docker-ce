package client

// Command returns a cli command handler if one exists
func (cli *DockerCli) Command(name string) func(...string) error {
	return map[string]func(...string) error{
		"attach":             cli.CmdAttach,
		"build":              cli.CmdBuild,
		"commit":             cli.CmdCommit,
		"cp":                 cli.CmdCp,
		"diff":               cli.CmdDiff,
		"events":             cli.CmdEvents,
		"exec":               cli.CmdExec,
		"history":            cli.CmdHistory,
		"images":             cli.CmdImages,
		"import":             cli.CmdImport,
		"info":               cli.CmdInfo,
		"inspect":            cli.CmdInspect,
		"kill":               cli.CmdKill,
		"load":               cli.CmdLoad,
		"login":              cli.CmdLogin,
		"logout":             cli.CmdLogout,
		"logs":               cli.CmdLogs,
		"network":            cli.CmdNetwork,
		"network create":     cli.CmdNetworkCreate,
		"network connect":    cli.CmdNetworkConnect,
		"network disconnect": cli.CmdNetworkDisconnect,
		"network inspect":    cli.CmdNetworkInspect,
		"network ls":         cli.CmdNetworkLs,
		"network rm":         cli.CmdNetworkRm,
		"pause":              cli.CmdPause,
		"port":               cli.CmdPort,
		"ps":                 cli.CmdPs,
		"pull":               cli.CmdPull,
		"push":               cli.CmdPush,
		"rename":             cli.CmdRename,
		"restart":            cli.CmdRestart,
		"rm":                 cli.CmdRm,
		"save":               cli.CmdSave,
		"stats":              cli.CmdStats,
		"tag":                cli.CmdTag,
		"top":                cli.CmdTop,
		"unpause":            cli.CmdUnpause,
		"update":             cli.CmdUpdate,
		"version":            cli.CmdVersion,
		"wait":               cli.CmdWait,
	}[name]
}
