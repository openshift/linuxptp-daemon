package features

var ocpFeatureMatrix = map[string]Features{
	"4.12": {
		OC: OCFeatures{
			Enabled: true,
		},
		BC: BCFeatures{
			Enabled: true,
		},
	},
	"4.13": {
		OC: OCFeatures{
			Enabled: true,
		},
		BC: BCFeatures{
			Enabled:  true,
			DualPort: true,
		},
	},
	"4.14": {
		OC: OCFeatures{
			Enabled: true,
		},
		BC: BCFeatures{
			Enabled:  true,
			DualPort: true,
		},
		GM: GMFeatures{
			Enabled: true,
		},
	},
	"4.15": {
		OC: OCFeatures{
			Enabled: true,
		},
		BC: BCFeatures{
			Enabled:  true,
			DualPort: true,
		},
		GM: GMFeatures{
			Enabled: true,
		},
	},
	"4.16": {
		OC: OCFeatures{
			Enabled: true,
		},
		BC: BCFeatures{
			Enabled:  true,
			DualPort: true,
			PTPHA:    true,
		},
		GM: GMFeatures{
			Enabled:  true,
			HoldOver: true,
			SyncE:    true,
		},
		LogSeverity: true,
	},
	"4.17": {
		OC: OCFeatures{
			Enabled: true,
		},
		BC: BCFeatures{
			Enabled:  true,
			DualPort: true,
			PTPHA:    true,
		},
		GM: GMFeatures{
			Enabled:  true,
			HoldOver: true,
			SyncE:    true,
		},
		LogSeverity: true,
	},
	"4.18": {
		OC: OCFeatures{
			Enabled: true,
		},
		BC: BCFeatures{
			Enabled:  true,
			DualPort: true,
			PTPHA:    true,
		},
		GM: GMFeatures{
			Enabled:  true,
			HoldOver: true,
			SyncE:    true,
		},
		LogSeverity: true,
	},
	"4.19": {
		OC: OCFeatures{
			Enabled:  true,
			DualPort: true,
		},
		BC: BCFeatures{
			Enabled:  true,
			DualPort: true,
			PTPHA:    true,
		},
		GM: GMFeatures{
			Enabled:  true,
			HoldOver: true,
			SyncE:    true,
		},
		LogSeverity: true,
	},
	"4.20": {
		OC: OCFeatures{
			Enabled:  true,
			DualPort: true,
		},
		BC: BCFeatures{
			Enabled:  true,
			DualPort: true,
			PTPHA:    true,
		},
		GM: GMFeatures{
			Enabled:  true,
			HoldOver: true,
			SyncE:    true,
		},
		LogSeverity: true,
	},
	// UPDATE LatestOCPInMatrix
}

// LatestOCPInMatrix acts as the default if key is not found
var LatestOCPInMatrix = "4.20"

func getOCPFeatures(ocpVersion string) Features {
	res, ok := ocpFeatureMatrix[ocpVersion]
	if !ok {
		return ocpFeatureMatrix[LatestOCPInMatrix]
	}
	return res
}
