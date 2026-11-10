package client

// MessageOwnerEnvVar overrides the config file's message_owner for one
// process: exactly 1, true or on turns it on; any other non-empty value
// turns it off.
const MessageOwnerEnvVar = "SOCK_MCP_MESSAGE_OWNER"

// Where MessageOwner's answer came from.
const (
	MessageOwnerEnv     = "env"
	MessageOwnerFile    = "file"
	MessageOwnerDefault = "default"
)

// MessageOwner reports whether this machine's puppets may message the owner
// without the owner writing first, and which source said so: the env
// override, else the file's message_owner, else on. An env value that is set
// but not 1, true or on is off: it fails closed, so a misspelt "no" or an
// unsubstituted template never falls through to the default.
func MessageOwner(getenv func(string) string, f FileConfig) (on bool, from string) {
	switch v := getenv(MessageOwnerEnvVar); v {
	case "":
	case "1", "true", "on":
		return true, MessageOwnerEnv
	default:
		return false, MessageOwnerEnv
	}
	if f.MessageOwner != nil {
		return *f.MessageOwner, MessageOwnerFile
	}
	return true, MessageOwnerDefault
}
