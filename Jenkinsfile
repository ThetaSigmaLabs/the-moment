pipeline {
    agent none

    environment {
        REGISTRY = '10.9.8.8:5050'
        IMAGE    = 'the-moment'
        TAG      = "${BUILD_NUMBER}"
        PATH     = '/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin'
    }

    stages {
        // ── Gate: tests must pass before any builds ────────────────────────
        stage('Tests') {
            agent { label 'linux-arm64' }
            steps {
                sh 'make test-all'
            }
        }

        // ── Build linux/arm64 binary (native) ─────────────────────────────
        stage('Build Binaries') {
            agent { label 'linux-arm64' }
            steps {
                sh 'CGO_ENABLED=1 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o the-moment-linux-arm64 .'
                stash name: 'bin-linux-arm64', includes: 'the-moment-linux-arm64'
            }
        }

        // ── Build arm64-only Docker image, push to local registry ──────────
        // Use plain docker build (not buildx) so the daemon's insecure-registry
        // config is inherited. BuildKit is on by default in Docker 23+.
        stage('Build Docker Image') {
            agent { label 'linux-arm64' }
            steps {
                sh '''
                    docker build \
                      --target production \
                      -t ${REGISTRY}/${IMAGE}:${TAG} \
                      -t ${REGISTRY}/${IMAGE}:latest \
                      .
                    docker push ${REGISTRY}/${IMAGE}:${TAG}
                    docker push ${REGISTRY}/${IMAGE}:latest
                '''
            }
        }

        // ── Smoke-test binary ──────────────────────────────────────────────
        stage('Test Binaries') {
            agent { label 'linux-arm64' }
            steps {
                unstash 'bin-linux-arm64'
                sh '''
                    chmod +x the-moment-linux-arm64
                    mkdir -p /tmp/tm-test-${BUILD_NUMBER}-arm64
                    THE_MOMENT_DB_PATH=/tmp/tm-test-${BUILD_NUMBER}-arm64 \
                      ./the-moment-linux-arm64 --port 15101 &
                    PID=$!
                    HTTP="000"
                    for i in $(seq 1 15); do
                      sleep 1
                      HTTP=$(curl -s -o /dev/null -w "%{http_code}" \
                        http://localhost:15101/api/status 2>/dev/null) || true
                      [ "$HTTP" = "200" ] && break
                    done
                    kill $PID 2>/dev/null || true
                    rm -rf /tmp/tm-test-${BUILD_NUMBER}-arm64
                    [ "$HTTP" = "200" ] || (echo "linux-arm64 smoke FAILED: HTTP $HTTP" && exit 1)
                '''
            }
        }

        // ── Smoke-test Docker image from registry ──────────────────────────
        stage('Test Docker Image') {
            agent { label 'linux-arm64' }
            steps {
                sh '''
                    docker pull ${REGISTRY}/${IMAGE}:${TAG}

                    docker run -d \
                      --name tm-docker-${BUILD_NUMBER} \
                      -e THE_MOMENT_DB_PATH=/app/data/db \
                      -e SPOOLMAN_URL=http://127.0.0.1:9999 \
                      -p 15200:5000 \
                      ${REGISTRY}/${IMAGE}:${TAG}

                    HTTP="000"
                    for i in $(seq 1 20); do
                      sleep 2
                      HTTP=$(curl -s -o /dev/null -w "%{http_code}" \
                        http://localhost:15200/api/status 2>/dev/null) || true
                      [ "$HTTP" = "200" ] && break
                    done
                    echo "=== Container logs ==="
                    docker logs tm-docker-${BUILD_NUMBER} 2>&1 || true
                    echo "=== End container logs ==="
                    docker rm -f tm-docker-${BUILD_NUMBER} || true
                    [ "$HTTP" = "200" ] || (echo "Docker smoke test FAILED: HTTP $HTTP" && exit 1)
                '''
            }
        }

        // ── Archive binaries ───────────────────────────────────────────────
        stage('Archive') {
            agent { label 'linux-arm64' }
            steps {
                unstash 'bin-linux-arm64'
                sh 'sha256sum the-moment-linux-arm64 > checksums.txt && cat checksums.txt'
                archiveArtifacts artifacts: 'the-moment-linux-arm64, checksums.txt', fingerprint: true
            }
        }
    }

    post {
        failure { echo 'Pipeline FAILED — check stage logs above.' }
        success { echo "Phase 1 complete. Image at ${REGISTRY}/${IMAGE}:${TAG} (linux/arm64)" }
    }
}
