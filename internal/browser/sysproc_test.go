package browser_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/browser"
)

func TestSysProcAttrStartsOwnGroup(t *testing.T) {
	require.True(t, browser.SysProcAttr().Setpgid)
}
