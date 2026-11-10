package client

// MessageOwnerEnvVar overrides the config file's message_owner for one
// process: exactly 1 or true turns it on, 0 or false off.
const MessageOwnerEnvVar = "SOCK_MCP_MESSAGE_OWNER"

// Where MessageOwner's answer came from.
const (
	MessageOwnerEnv     = "env"
	MessageOwnerFile    = "file"
	MessageOwnerDefault = "default"
)

// MessageOwner reports whether this machine's puppets may message the owner
// without the owner writing first, and which source said so: the env
// override, else the file's message_owner, else on. Any other env value is
// not a setting and is ignored.
func MessageOwner(getenv func(string) string, f FileConfig) (on bool, from string) {
	switch getenv(MessageOwnerEnvVar) {
	case "1", "true":
		return true, MessageOwnerEnv
	case "0", "false":
		return false, MessageOwnerEnv
	}
	if f.MessageOwner != nil {
		return *f.MessageOwner, MessageOwnerFile
	}
	return true, MessageOwnerDefault
}
