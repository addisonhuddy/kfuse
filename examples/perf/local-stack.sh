#!/usr/bin/env bash
# Credential-free local stack for the kfuse benchmark suite: Apache Kafka
# (KRaft, SASL/PLAIN) + MinIO impersonating the real S3 endpoint via DNS +
# a self-signed cert. The unmodified kfuse binary works against it.
#
#   ./examples/perf/local-stack.sh up      start containers, print env exports
#   ./examples/perf/local-stack.sh down    stop containers
#
# `up` writes the environment to examples/perf/.stack-env; source it before
# running run.sh. Needs docker and (once) sudo to add an /etc/hosts entry.
set -euo pipefail
cd "$(dirname "$0")"

BUCKET=${KF_PERF_BUCKET:-kfuse-perf}
STACK_DIR=/tmp/kfuse-perf-stack

down() {
  docker rm -f kfuse-perf-kafka kfuse-perf-minio >/dev/null 2>&1 || true
}

up() {
  down
  mkdir -p "$STACK_DIR/jaas" "$STACK_DIR/certs" "$STACK_DIR/miodata"

  cat > "$STACK_DIR/jaas/kafka_server_jaas.conf" <<'EOF'
KafkaServer {
  org.apache.kafka.common.security.plain.PlainLoginModule required
  username="kfuse" password="kfusepass123" user_kfuse="kfusepass123";
};
EOF
  docker run -d --name kfuse-perf-kafka --network host \
    -v "$STACK_DIR/jaas":/etc/kafka/jaas:ro \
    -e KAFKA_OPTS='-Djava.security.auth.login.config=/etc/kafka/jaas/kafka_server_jaas.conf' \
    -e CLUSTER_ID=5L6g3nShT-eMCtKzzX86sw -e KAFKA_NODE_ID=1 \
    -e KAFKA_PROCESS_ROLES=broker,controller \
    -e KAFKA_LISTENERS='SASL_PLAINTEXT://:9092,CONTROLLER://:9093' \
    -e KAFKA_ADVERTISED_LISTENERS='SASL_PLAINTEXT://127.0.0.1:9092' \
    -e KAFKA_LISTENER_SECURITY_PROTOCOL_MAP='CONTROLLER:PLAINTEXT,SASL_PLAINTEXT:SASL_PLAINTEXT' \
    -e KAFKA_CONTROLLER_LISTENER_NAMES=CONTROLLER \
    -e KAFKA_CONTROLLER_QUORUM_VOTERS='1@localhost:9093' \
    -e KAFKA_INTER_BROKER_LISTENER_NAME=SASL_PLAINTEXT \
    -e KAFKA_SASL_ENABLED_MECHANISMS=PLAIN \
    -e KAFKA_SASL_MECHANISM_INTER_BROKER_PROTOCOL=PLAIN \
    -e KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR=1 \
    -e KAFKA_TRANSACTION_STATE_LOG_REPLICATION_FACTOR=1 \
    -e KAFKA_TRANSACTION_STATE_LOG_MIN_ISR=1 -e KAFKA_NUM_PARTITIONS=8 \
    apache/kafka:latest >/dev/null

  if [ ! -f "$STACK_DIR/certs/public.crt" ]; then
    openssl req -x509 -newkey rsa:2048 -sha256 -days 30 -nodes \
      -keyout "$STACK_DIR/certs/private.key" -out "$STACK_DIR/certs/public.crt" \
      -subj "/CN=s3.us-east-1.amazonaws.com" \
      -addext "subjectAltName=DNS:s3.us-east-1.amazonaws.com,DNS:*.s3.us-east-1.amazonaws.com" \
      2>/dev/null
  fi
  docker run -d --name kfuse-perf-minio --network host \
    -e MINIO_ROOT_USER=kfuseakid -e MINIO_ROOT_PASSWORD=kfusesecret123 \
    -e MINIO_DOMAIN=s3.us-east-1.amazonaws.com \
    -v "$STACK_DIR/certs":/certs:ro -v "$STACK_DIR/miodata":/data \
    minio/minio:latest server /data --address :443 --certs-dir /certs >/dev/null

  if ! grep -q "s3.us-east-1.amazonaws.com" /etc/hosts; then
    echo "127.0.0.1 s3.us-east-1.amazonaws.com $BUCKET.s3.us-east-1.amazonaws.com" | sudo tee -a /etc/hosts >/dev/null
  fi

  mkdir -p "$STACK_DIR/miodata/$BUCKET"   # MinIO treats a data dir as a bucket

  cat > .stack-env <<EOF
export KF_KAFKA_TLS=false
export KF_KAFKA_BROKERS=127.0.0.1:9092
export KF_KAFKA_SASL_USERNAME=kfuse
export KF_KAFKA_SASL_PASSWORD=kfusepass123
export AWS_REGION=us-east-1
export AWS_ACCESS_KEY_ID=kfuseakid
export AWS_SECRET_ACCESS_KEY=kfusesecret123
export KF_BLOB_BUCKET=$BUCKET
export SSL_CERT_FILE=$STACK_DIR/certs/public.crt
EOF
  echo "local-stack: up. Run: source examples/perf/.stack-env"
}

case "${1:-}" in
  up) up ;;
  down) down; echo "local-stack: down" ;;
  *) echo "usage: local-stack.sh <up|down>"; exit 2 ;;
esac
