package hardwareconfig

import (
	"context"
	"strings"

	"github.com/golang/glog"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"
)

const (
	defaultConfigMapName = "board-label-mapping"
)

// BoardLabelMap maps old board labels to new board labels
type BoardLabelMap map[string]string

// BoardLabelMapLoader loads board label remapping from ConfigMap
type BoardLabelMapLoader struct {
	client        kubernetes.Interface
	namespace     string
	configMapName string
}

// NewBoardLabelMapLoader creates a new ConfigMap loader
func NewBoardLabelMapLoader(kubeClient kubernetes.Interface, namespace string) *BoardLabelMapLoader {
	return &BoardLabelMapLoader{
		client:        kubeClient,
		namespace:     namespace,
		configMapName: defaultConfigMapName,
	}
}

// LoadBoardLabelMap loads board label remapping map for a given hwDefPath from ConfigMap
// Returns nil if ConfigMap not found or key doesn't exist (not an error - remapping is optional)
func (l *BoardLabelMapLoader) LoadBoardLabelMap(hwDefPath string) (BoardLabelMap, error) {
	// Load ConfigMap
	cm, err := l.client.CoreV1().ConfigMaps(l.namespace).Get(context.TODO(), l.configMapName, metav1.GetOptions{})
	if err != nil {
		glog.V(4).Infof("ConfigMap %s/%s not found (board label remapping disabled): %v", l.namespace, l.configMapName, err)
		return nil, nil
	}

	// Convert hwDefPath to a valid ConfigMap key (replace '/' with '-')
	// ConfigMap keys must match regex: [-._a-zA-Z0-9]+
	key := strings.ReplaceAll(hwDefPath, "/", "-")
	data, ok := cm.Data[key]
	if !ok {
		glog.V(4).Infof("No board label map found for %s in ConfigMap %s/%s", hwDefPath, l.namespace, l.configMapName)
		return nil, nil
	}

	// Parse YAML
	var config struct {
		BoardLabelMap BoardLabelMap `yaml:"boardLabelMap"`
	}
	if err = yaml.Unmarshal([]byte(data), &config); err != nil {
		glog.Warningf("Failed to parse board label map for %s: %v (using embedded defaults)", hwDefPath, err)
		return nil, nil
	}

	// Return the map (empty map if nil)
	if config.BoardLabelMap == nil {
		config.BoardLabelMap = make(BoardLabelMap)
	}
	glog.Infof("Loaded board label map for %s: %d mappings", hwDefPath, len(config.BoardLabelMap))
	return config.BoardLabelMap, nil
}
