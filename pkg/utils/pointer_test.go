package utils_test

import (
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	"github.com/stretchr/testify/assert"
)

func TestToPtrReturnsIndependentCopy(t *testing.T) {
	value := 5
	first, second := utils.ToPtr(value), utils.ToPtr(value)
	*first = 10
	assert.Equal(t, 5, value)
	assert.Equal(t, 5, *second)
	assert.NotSame(t, first, second)
}
