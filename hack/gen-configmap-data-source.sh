#!/bin/bash

CTRL=kubectl
CONFIG_MAP_DIR="linuxptp-configmap"
mkdir -p $CONFIG_MAP_DIR

nodes=$($CTRL get nodes -o jsonpath="{.items[*].metadata.name}")
array=(`echo $nodes | sed 's/ /\n/g'`)
for node_name in "${array[@]}"
do
	echo $node_name
	touch "$CONFIG_MAP_DIR/$node_name"
	echo '{"interface":"eth0", "ptp4lOpts":"-s -2", "phc2sysOpts":"-a -r"}' > "$CONFIG_MAP_DIR/$node_name"
done
