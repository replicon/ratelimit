#!/bin/sh

if [ -n "${REDIS_AUTH_SSM_PATH:-}" ]; then
    export REDIS_AUTH=$(/usr/local/bin/aws ssm get-parameter --name ${REDIS_AUTH_SSM_PATH} --with-decryption --query 'Parameter.Value' --output text)
fi
nohup /sync_config.sh &
/bin/ratelimit
