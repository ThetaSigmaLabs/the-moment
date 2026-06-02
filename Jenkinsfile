pipeline {
    agent none

    environment {
        REGISTRY = '10.9.8.8:5050'
        IMAGE    = 'the-moment'
        TAG      = "${BUILD_NUMBER}"
        PATH+GO  = "/usr/local/go/bin"
    }

    stages {
        // ── Gate: tests must pass before any builds ────────────────────────
        stage('Tests') {
            agent { label 'linux-arm64' }
            steps {
                sh 'go test ./... -count=1'
                sh 'go test -tags=integration ./... -count=1 -v'
            }
        }

        // ── Build platform binaries ────────────────────────────────────────
        stage('Build Binaries') {
            parallel {
                stage('Linux ARM64') {
                    agent { label 'linux-arm64' }
                    steps {
                        sh 'CGO_ENABLED=1 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o the-moment-linux-arm64 .'
                        stash name: 'bin-linux-arm64', includes: 'the-moment-linux-arm64'
                    }
                }

                // linux/amd64: build inside a golang:alpine container via QEMU
                stage('Linux AMD64') {
                    agent { label 'linux-arm64' }
                    steps {
                        sh '''
                            docker run --rm --platform linux/amd64 \
                              -v "$WORKSPACE":/src -w /src \
                              golang:1.24-alpine \
                              sh -c "apk add --no-cache gcc musl-dev && \
                                     CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
                                     go build -ldflags='-s -w' -o the-moment-linux-amd64 ."
                        '''
                        stash name: 'bin-linux-amd64', includes: 'the-moment-linux-amd64'
                    }
                }
            }
        }

        // ── Build multi-arch Docker image, push to local registry ──────────
        stage('Build Docker Image') {
            agent { label 'linux-arm64' }
            steps {
                sh '''
                    docker buildx create --name ci-builder --use 2>/dev/null || \
                      docker buildx use ci-builder

                    docker buildx build \
                      --platform linux/amd64,linux/arm64 \
                      -t ${REGISTRY}/${IMAGE}:${TAG} \
                      -t ${REGISTRY}/${IMAGE}:latest \
                      --push \
                      .
                '''
            }
        }

        // ── Smoke-test binaries ────────────────────────────────────────────
        stage('Test Binaries') {
            parallel {
                stage('Test: Linux ARM64') {
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

                stage('Test: Linux AMD64') {
                    agent { label 'linux-arm64' }
                    steps {
                        unstash 'bin-linux-amd64'
                        sh '''
                            chmod +x the-moment-linux-amd64
                            docker run -d \
                              --name tm-amd64-${BUILD_NUMBER} \
                              --platform linux/amd64 \
                              -v "$WORKSPACE/the-moment-linux-amd64":/usr/local/bin/tm \
                              -e THE_MOMENT_DB_PATH=/tmp/tm-test \
                              -p 15102:15102 \
                              alpine sh -c "mkdir -p /tmp/tm-test && /usr/local/bin/tm --port 15102"

                            HTTP="000"
                            for i in $(seq 1 15); do
                              sleep 1
                              HTTP=$(curl -s -o /dev/null -w "%{http_code}" \
                                http://localhost:15102/api/status 2>/dev/null) || true
                              [ "$HTTP" = "200" ] && break
                            done
                            docker rm -f tm-amd64-${BUILD_NUMBER} || true
                            [ "$HTTP" = "200" ] || (echo "linux-amd64 smoke FAILED: HTTP $HTTP" && exit 1)
                        '''
                    }
                }
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
                unstash 'bin-linux-amd64'
                sh 'sha256sum the-moment-linux-* > checksums.txt && cat checksums.txt'
                archiveArtifacts artifacts: 'the-moment-linux-*, checksums.txt', fingerprint: true
            }
        }
    }

    post {
        failure { echo 'Pipeline FAILED — check stage logs above.' }
        success { echo "Phase 1 complete. Images at ${REGISTRY}/${IMAGE}:${TAG}" }
    }
}
