#!/bin/bash

# Check for ptp4l process
if pgrep ptp4l > /dev/null 2>&1; then
  # Terminate ptp4l process
  pkill -f ptp4l
  echo "ptp4l process terminated."
fi

# Check for phc2sys process
if pgrep phc2sys > /dev/null 2>&1; then
  # Terminate phc2sys process
  pkill -f phc2sys
  echo "phc2sys process terminated."
fi

# Check for ts2phc process
if pgrep ts2phc > /dev/null 2>&1; then
  # Terminate ts2phc process
  pkill -f ts2phc
  echo "ts2phc process terminated."
fi

# Remove configuration files
rm -f ptp4l.*.config phc2sys.*.config

# Remove potential socket files (assuming they start with "ptp4l" or "phc2sys")
find . -name "ptp4l*" -type s -delete
find . -name "phc2sys*" -type s -delete

echo "Socket files removed (starting with ptp4l or phc2sys).
