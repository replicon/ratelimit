#!/bin/bash
function installLamp()
{
    apt-get install -y wget 
    wget -q https://s3-us-west-2.amazonaws.com/opsgeniedownloads/repo/opsgenie-lamp-2.5.0.zip
    unzip opsgenie-lamp-2.5.0.zip -d opsgenie
    mv opsgenie/lamp/lamp /usr/local/bin
    rm -rf opsgenie*
}
function setEnvs()
{
. ci/set_env.sh
}
set -eo nounset

setEnvs

installLamp &
docker login -u "$DOCKER_USER" -p "$DOCKER_PASSWORD"
eval $(aws ecr get-login --region us-west-2 --no-include-email)
for img in "golang:1.14" "alpine:3.11"
do
    docker pull $img
done
wait

set -e
apt-get update -y
apt-get install -y  redis-server

make tests_with_redis

docker build -t $ECR_REPO:b-$VERSION .
if [ "$REPLICON_GIT_BRANCH" = "master" ]
then
docker tag $ECR_REPO:b-$VERSION $ECR_REPO:m-$VERSION
docker push $ECR_REPO:m-$VERSION
else
#Push branch
echo "Branch Build"
docker push $ECR_REPO:b-$VERSION
fi
