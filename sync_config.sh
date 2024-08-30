#!/bin/sh

export AWS_USE_FIPS_ENDPOINT=${AWS_USE_FIPS_ENDPOINT}

/bin/aws --version

mkdir -p /extra_metrics
/bin/python3 /metrics/main.py >/dev/null 2>&1 &

cd /srv/runtime_data/current

while true
do
  /bin/aws s3 sync --delete ${RLS_S3_PATH} data/

  [ $? -eq 0 ] && /bin/python3 data/${RLS_SCRIPT_DIR}config-generator.py \
    --no-docker-container \
    --static data/${RLS_STATIC_DIR} \
    --blacklist data/${RLS_BLACKLIST_DIR} \
    --override data/${RLS_OVERRIDE_DIR} \
    --output /srv/runtime_data/current/validate_config/new_config.yaml \
    --node-exporter-textfile /extra_metrics \
    --swimlane ${SWIMLANE} > /dev/null

  [ $? -eq 0 ] && mv validate_config/new_config.yaml config/config.yaml

  sleep 300
done
