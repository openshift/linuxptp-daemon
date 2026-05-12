package cep

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
)

func TestEventCache_Get(t *testing.T) {
	t.Run("Get returns false for unknown type", func(t *testing.T) {
		cache := NewEventCache("worker-0")
		_, ok := cache.Get("nonexistent")
		assert.False(t, ok)
	})
}

func TestEventCache_GetBySource(t *testing.T) {
	t.Run("GetBySource returns matching event", func(t *testing.T) {
		cache := NewEventCache("worker-0")
		proxy := NewCloudEventProxy(cache, nil)

		var buf bytes.Buffer
		ipc.Encode(&buf, []ipc.Message{{
			Version: ipc.Version, Type: ipc.TypePTPState,
			Profile: testProfile, IFace: testIFace,
			Values: ipc.StateValue{State: ipc.StateLocked},
		}})
		proxy.Listen(&buf)

		e, ok := cache.GetBySource(testPTPLockState)
		require.True(t, ok)
		assert.Equal(t, testPTPLockState, e.Source)
		assert.Equal(t, ipc.StateLocked, e.Data.Values[0].Value)
	})

	t.Run("GetBySource returns false for unknown source", func(t *testing.T) {
		cache := NewEventCache("worker-0")
		_, ok := cache.GetBySource("/nonexistent/path")
		assert.False(t, ok)
	})
}

func ptpMsg(iface, state string) ipc.Message {
	return ipc.Message{
		Version: ipc.Version, Type: ipc.TypePTPState,
		Profile: testProfile, IFace: iface,
		Values: ipc.StateValue{State: state},
	}
}

func TestEventCache_Update(t *testing.T) {
	t.Run("first event creates entry and returns non-nil", func(t *testing.T) {
		cache := NewEventCache("worker-0")
		result := cache.Update(ptpMsg(testIFace, ipc.StateLocked))

		require.NotNil(t, result)
		assert.Contains(t, result.Source, "worker-0")
		assert.NotEmpty(t, result.ID)
		assert.NotEmpty(t, result.Data.Values)
	})

	t.Run("duplicate state returns nil", func(t *testing.T) {
		cache := NewEventCache("worker-0")
		first := cache.Update(ptpMsg(testIFace, ipc.StateLocked))
		require.NotNil(t, first)

		second := cache.Update(ptpMsg(testIFace, ipc.StateLocked))
		assert.Nil(t, second)
	})

	t.Run("state change returns non-nil with new ID", func(t *testing.T) {
		cache := NewEventCache("worker-0")
		first := cache.Update(ptpMsg(testIFace, ipc.StateLocked))
		require.NotNil(t, first)

		second := cache.Update(ptpMsg(testIFace, ipc.StateFreerun))
		require.NotNil(t, second)
		assert.NotEqual(t, first.ID, second.ID)

		assert.Equal(t, ipc.StateFreerun, second.Data.Values[0].Value)
	})

	t.Run("different interfaces accumulate data values", func(t *testing.T) {
		cache := NewEventCache("worker-0")
		first := cache.Update(ptpMsg(testIFace, ipc.StateLocked))
		require.NotNil(t, first)
		firstDVCount := len(first.Data.Values)

		second := cache.Update(ptpMsg("ens3f0", ipc.StateHoldover))
		require.NotNil(t, second)
		assert.Greater(t, len(second.Data.Values), firstDVCount)

		cached, ok := cache.Get(ipc.TypePTPState)
		require.True(t, ok)
		assert.Equal(t, len(second.Data.Values), len(cached.Data.Values))
	})

	t.Run("same interface overwrites not appends", func(t *testing.T) {
		cache := NewEventCache("worker-0")
		first := cache.Update(ptpMsg(testIFace, ipc.StateLocked))
		require.NotNil(t, first)

		second := cache.Update(ptpMsg(testIFace, ipc.StateFreerun))
		require.NotNil(t, second)

		assert.Equal(t, len(first.Data.Values), len(second.Data.Values),
			"DV count should not grow when same interface changes state")
	})

	t.Run("different event types get separate cache entries", func(t *testing.T) {
		cache := NewEventCache("worker-0")
		cache.Update(ptpMsg(testIFace, ipc.StateLocked))
		cache.Update(ipc.Message{
			Version: ipc.Version, Type: ipc.TypeClockClass,
			Profile: testProfile,
			Values:  ipc.ClockClassValue{ClockClass: 6},
		})

		_, ok := cache.Get(ipc.TypePTPState)
		assert.True(t, ok)
		_, ok = cache.Get(ipc.TypeClockClass)
		assert.True(t, ok)
	})

	t.Run("no-interface event replaces all values on change", func(t *testing.T) {
		cache := NewEventCache("worker-0")
		first := cache.Update(ipc.Message{
			Version: ipc.Version, Type: ipc.TypeOSClockState,
			Profile: testProfile,
			Values:  ipc.StateValue{State: ipc.StateLocked},
		})
		require.NotNil(t, first)

		second := cache.Update(ipc.Message{
			Version: ipc.Version, Type: ipc.TypeOSClockState,
			Profile: testProfile,
			Values:  ipc.StateValue{State: ipc.StateFreerun},
		})
		require.NotNil(t, second)
		assert.Equal(t, ipc.StateFreerun, second.Data.Values[0].Value)
	})

	t.Run("no-interface multi-value detects change in non-first value", func(t *testing.T) {
		cache := NewEventCache("worker-0")
		first := cache.Update(ipc.Message{
			Version: ipc.Version, Type: ipc.TypeSyncEClockQuality,
			Profile: testProfile,
			Values:  ipc.SyncEClockQualityValue{QL: 2, ExtendedQL: 10},
		})
		require.NotNil(t, first)
		require.Len(t, first.Data.Values, 2)

		dup := cache.Update(ipc.Message{
			Version: ipc.Version, Type: ipc.TypeSyncEClockQuality,
			Profile: testProfile,
			Values:  ipc.SyncEClockQualityValue{QL: 2, ExtendedQL: 10},
		})
		assert.Nil(t, dup, "identical values should be detected as duplicate")

		changed := cache.Update(ipc.Message{
			Version: ipc.Version, Type: ipc.TypeSyncEClockQuality,
			Profile: testProfile,
			Values:  ipc.SyncEClockQualityValue{QL: 2, ExtendedQL: 20},
		})
		require.NotNil(t, changed, "change in second data value must not be treated as duplicate")
		assert.NotEqual(t, first.ID, changed.ID)
	})

	t.Run("unknown type returns nil", func(t *testing.T) {
		cache := NewEventCache("worker-0")
		result := cache.Update(ipc.Message{
			Version: ipc.Version, Type: "unknown_type",
		})
		assert.Nil(t, result)
	})

	t.Run("clear removes all entries", func(t *testing.T) {
		cache := NewEventCache("worker-0")
		cache.Update(ptpMsg(testIFace, ipc.StateLocked))

		cache.Clear()

		_, ok := cache.Get(ipc.TypePTPState)
		assert.False(t, ok)
	})
}
