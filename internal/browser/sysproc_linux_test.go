package browser_test

import (
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/browser"
)

// On Linux the kernel kills the browser if fbm dies without running Close.
func TestSysProcAttrKillsBrowserWithParent(t *testing.T) {
	require.Equal(t, syscall.SIGKILL, browser.SysProcAttr().Pdeathsig)
}
