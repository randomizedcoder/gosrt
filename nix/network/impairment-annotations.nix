# nix/network/impairment-annotations.nix
#
# Annotation API integration for impairment events.
# Every impairment scenario triggers a Grafana annotation when activated.
#
# Reference: documentation/nix_microvm_implementation_plan.md Step 2.5
#
{ pkgs, lib, metricsIp ? "10.50.8.2" }:

let
  grafanaUrl = "http://${metricsIp}:3000";
  grafanaAuth = "admin:srt";

in {
  # Create annotation helper script
  mkAnnotationScript = { name, tags ? [ "impairment" ] }: pkgs.writeShellApplication {
    name = "annotate-${name}";
    runtimeInputs = with pkgs; [ curl coreutils ];
    text = ''
      ACTION="''${1:-start}"
      TIMESTAMP=$(date +%s)000  # Grafana expects milliseconds

      # Build tags JSON array
      TAGS='["impairment", "${name}", "'"$ACTION"'"]'

      curl -s -X POST \
        -u "${grafanaAuth}" \
        -H "Content-Type: application/json" \
        -d '{
          "time": '"$TIMESTAMP"',
          "text": "Impairment ${name}: '"$ACTION"'",
          "tags": '"$TAGS"'
        }' \
        "${grafanaUrl}/api/annotations" >/dev/null

      echo "Annotation created: ${name} $ACTION"
    '';
  };

  # Wrap a scenario script with annotations
  # Usage: wrapWithAnnotations "starlink-handoff" applyScript cleanupScript
  wrapWithAnnotations = name: applyScript: cleanupScript: {
    apply = pkgs.writeShellApplication {
      name = "apply-${name}-annotated";
      runtimeInputs = with pkgs; [ curl coreutils ];
      text = ''
        # Create start annotation
        TIMESTAMP=$(date +%s)000
        curl -s -X POST \
          -u "${grafanaAuth}" \
          -H "Content-Type: application/json" \
          -d '{"time": '"$TIMESTAMP"', "text": "Impairment ${name}: START", "tags": ["impairment", "${name}", "start"]}' \
          "${grafanaUrl}/api/annotations" >/dev/null || true

        # Run the actual apply script
        ${applyScript}
      '';
    };

    cleanup = pkgs.writeShellApplication {
      name = "cleanup-${name}-annotated";
      runtimeInputs = with pkgs; [ curl coreutils ];
      text = ''
        # Run the actual cleanup script
        ${cleanupScript}

        # Create end annotation
        TIMESTAMP=$(date +%s)000
        curl -s -X POST \
          -u "${grafanaAuth}" \
          -H "Content-Type: application/json" \
          -d '{"time": '"$TIMESTAMP"', "text": "Impairment ${name}: END", "tags": ["impairment", "${name}", "end"]}' \
          "${grafanaUrl}/api/annotations" >/dev/null || true
      '';
    };
  };

  # Test script to verify annotation API is working
  testAnnotation = pkgs.writeShellApplication {
    name = "test-annotation";
    runtimeInputs = with pkgs; [ curl coreutils jq ];
    text = ''
      echo "Testing Grafana annotation API at ${grafanaUrl}..."

      # Create test annotation
      TIMESTAMP=$(date +%s)000
      RESULT=$(curl -s -X POST \
        -u "${grafanaAuth}" \
        -H "Content-Type: application/json" \
        -d '{"time": '"$TIMESTAMP"', "text": "Test annotation from Nix", "tags": ["test", "nix"]}' \
        "${grafanaUrl}/api/annotations")

      if echo "$RESULT" | jq -e '.id' >/dev/null 2>&1; then
        ID=$(echo "$RESULT" | jq -r '.id')
        echo "Success! Created annotation with ID: $ID"

        # Clean up test annotation
        curl -s -X DELETE \
          -u "${grafanaAuth}" \
          "${grafanaUrl}/api/annotations/$ID" >/dev/null
        echo "Cleaned up test annotation"
      else
        echo "Failed to create annotation:"
        echo "$RESULT"
        exit 1
      fi
    '';
  };
}
