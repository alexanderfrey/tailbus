#!/bin/sh
set -e

# Register agents for a given machine role.
# Usage: register-agents.sh <machine-a|machine-b|machine-c>
#
# Requires: tailbus binary in PATH and tailbusd running locally.

ROLE="${1:?Usage: register-agents.sh <machine-a|machine-b|machine-c>}"
SOCKET="${TAILBUS_SOCKET:-/tmp/tailbusd.sock}"

register() {
  tailbus -socket "$SOCKET" register "$@"
}

case "$ROLE" in
  machine-a)
    register concierge \
      -description "Travel concierge — orchestrates bookings across services" \
      -tags "travel,orchestrator" \
      -version "1.0"
    ;;

  machine-b)
    register flights \
      -description "Flight search and booking" \
      -tags "travel,booking" \
      -version "1.0"

    register hotels \
      -description "Hotel search and booking" \
      -tags "travel,booking" \
      -version "1.0"
    ;;

  machine-c)
    register weather \
      -description "Weather forecasts for travel destinations" \
      -tags "travel,data" \
      -version "1.0"

    register currency \
      -description "Currency exchange rates and conversion" \
      -tags "travel,data" \
      -version "1.0"
    ;;

  *)
    echo "Unknown role: $ROLE"
    echo "Valid roles: machine-a, machine-b, machine-c"
    exit 1
    ;;
esac

echo ""
echo "Agents registered for $ROLE. Verify with:"
echo "  tailbus -socket $SOCKET list"
