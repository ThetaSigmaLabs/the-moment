pipeline {
    agent none

    environment {
        REGISTRY = '10.9.8.8:5050'
        IMAGE    = 'the-moment'
        TAG      = "${BUILD_NUMBER}"
    }

    stages {

        // ── Gate: tests must pass on both platforms before any builds ─────────
        stage('Tests') {
            parallel {
                stage('Tests: linux/arm64') {
                    agent { label 'linux-arm64' }
                    options { skipDefaultCheckout() }
                    environment {
                        PATH = '/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin'
                    }
                    steps {
                        cleanWs()
                        checkout scm
                        sh 'make test-all'
                    }
                }
                stage('Tests: windows/amd64') {
                    agent { label 'windows' }
                    options { skipDefaultCheckout() }
                    steps {
                        cleanWs()
                        checkout scm
                        // Verify tools are reachable under the NSSM service account PATH
                        bat 'go version'
                        bat 'gcc --version'
                        // make is not guaranteed on Windows — run the two targets directly
                        // CGO_ENABLED=1 is explicit: mattn/go-sqlite3 requires gcc
                        bat 'set CGO_ENABLED=1&& go test ./... -count=1'
                        bat 'set CGO_ENABLED=1&& go test -tags=integration ./... -count=1 -v'
                    }
                }
            }
        }

        // ── Build platform binaries ───────────────────────────────────────────
        // linux/arm64 — native CGO build on ARM64 agent
        // windows/amd64 — native CGO build on Windows agent (requires MinGW gcc)
        // linux/amd64 binary — compiled inside the Docker image; no standalone artifact
        stage('Build Binaries') {
            parallel {
                stage('linux/arm64') {
                    agent { label 'linux-arm64' }
                    options { skipDefaultCheckout() }
                    environment {
                        PATH = '/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin'
                    }
                    steps {
                        cleanWs()
                        checkout scm
                        sh 'CGO_ENABLED=1 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o the-moment-linux-arm64 .'
                        sh 'file the-moment-linux-arm64'
                        stash name: 'bin-linux-arm64', includes: 'the-moment-linux-arm64'
                    }
                }
                stage('windows/amd64') {
                    agent { label 'windows' }
                    options { skipDefaultCheckout() }
                    steps {
                        cleanWs()
                        checkout scm
                        // windows/amd64 — native build, CGO_ENABLED=1 requires MinGW gcc in PATH
                        // CC=gcc is explicit so Go doesn't silently fall back to a wrong compiler
                        powershell '''
                            $env:CGO_ENABLED = "1"
                            $env:CC          = "gcc"
                            $env:GOOS        = "windows"
                            $env:GOARCH      = "amd64"
                            go build -ldflags="-s -w" -o the-moment-windows-amd64.exe .
                        '''
                        bat 'dir the-moment-windows-amd64.exe'
                        stash name: 'bin-windows-amd64', includes: 'the-moment-windows-amd64.exe'
                    }
                }
            }
        }

        // ── Build per-platform Docker images, push with arch suffix ──────────
        // ARM64: plain docker build on the ARM64 agent (inherits daemon.json insecure-registry).
        // amd64: docker build --platform linux/amd64 on Windows via Docker Desktop.
        //        The linux/amd64 binary is compiled inside the container by the Dockerfile.
        // Multi-arch manifest is assembled in the next stage.
        stage('Build Docker Images') {
            parallel {
                stage('Docker: linux/arm64') {
                    agent { label 'linux-arm64' }
                    options { skipDefaultCheckout() }
                    environment {
                        PATH = '/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin'
                    }
                    steps {
                        cleanWs()
                        checkout scm
                        unstash 'bin-linux-arm64'
                        sh '''
                            cp the-moment-linux-arm64 main
                            docker build --platform linux/arm64 --target production-prebuilt \
                              -t ${REGISTRY}/${IMAGE}:${TAG}-arm64 \
                              .
                            docker push ${REGISTRY}/${IMAGE}:${TAG}-arm64
                            echo "Pushed ${REGISTRY}/${IMAGE}:${TAG}-arm64"
                        '''
                    }
                }
                stage('Docker: linux/amd64') {
                    agent { label 'windows' }
                    options { skipDefaultCheckout() }
                    steps {
                        cleanWs()
                        checkout scm
                        powershell '''
                            $img = "$env:REGISTRY/$($env:IMAGE):$($env:TAG)-amd64"
                            docker build --target production --platform linux/amd64 -t $img .
                            docker push $img
                            Write-Host "Pushed $img"
                        '''
                    }
                }
            }
        }

        // ── Combine into a multi-arch manifest ───────────────────────────────
        // Uses buildx imagetools (requires a builder that knows about the insecure
        // registry — created here with buildkitd.toml, removed after use).
        stage('Create Multi-arch Manifest') {
            agent { label 'linux-arm64' }
            options { skipDefaultCheckout() }
            environment {
                PATH = '/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin'
            }
            steps {
                cleanWs()
                sh '''
                    cat > /tmp/buildkitd-${BUILD_NUMBER}.toml <<EOF
[registry."${REGISTRY}"]
  http = true
  insecure = true
EOF
                    docker buildx create --name ci-manifest-${BUILD_NUMBER} \
                      --driver-opt network=host \
                      --config /tmp/buildkitd-${BUILD_NUMBER}.toml \
                      --use

                    docker buildx imagetools create \
                      -t ${REGISTRY}/${IMAGE}:${TAG} \
                      -t ${REGISTRY}/${IMAGE}:latest \
                      ${REGISTRY}/${IMAGE}:${TAG}-arm64 \
                      ${REGISTRY}/${IMAGE}:${TAG}-amd64

                    echo "=== Manifest verification ==="
                    docker buildx imagetools inspect ${REGISTRY}/${IMAGE}:${TAG}

                    docker buildx rm ci-manifest-${BUILD_NUMBER} || true
                    rm -f /tmp/buildkitd-${BUILD_NUMBER}.toml
                '''
            }
        }

        // ── Smoke-test binaries on their native platforms ─────────────────────
        stage('Test Binaries') {
            parallel {
                stage('Test: linux/arm64') {
                    agent { label 'linux-arm64' }
                    options { skipDefaultCheckout() }
                    environment {
                        PATH = '/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin'
                    }
                    steps {
                        cleanWs()
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
                            [ "$HTTP" = "200" ] || (echo "linux/arm64 smoke FAILED: HTTP $HTTP" && exit 1)
                            echo "linux/arm64 smoke: PASSED (HTTP $HTTP)"
                        '''
                    }
                }
                stage('Test: windows/amd64') {
                    agent { label 'windows' }
                    options { skipDefaultCheckout() }
                    steps {
                        cleanWs()
                        unstash 'bin-windows-amd64'
                        powershell '''
                            $tmpDir = "$env:TEMP\\tm-test-$env:BUILD_NUMBER-win"
                            New-Item -ItemType Directory -Force $tmpDir | Out-Null
                            $env:THE_MOMENT_DB_PATH = $tmpDir
                            $proc = Start-Process -FilePath ".\\the-moment-windows-amd64.exe" `
                                        -ArgumentList "--port","15102" `
                                        -PassThru
                            $http = "000"
                            for ($i = 1; $i -le 15; $i++) {
                                Start-Sleep 1
                                try {
                                    $r = Invoke-WebRequest -Uri "http://localhost:15102/api/status" `
                                             -UseBasicParsing -TimeoutSec 2
                                    $http = [string]$r.StatusCode
                                } catch {}
                                if ($http -eq "200") { break }
                            }
                            Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue
                            Remove-Item -Recurse -Force $tmpDir -ErrorAction SilentlyContinue
                            if ($http -ne "200") {
                                Write-Error "windows/amd64 smoke FAILED: HTTP $http"
                                exit 1
                            }
                            Write-Host "windows/amd64 smoke: PASSED (HTTP $http)"
                        '''
                    }
                }
            }
        }

        // ── Smoke-test Docker images from registry ────────────────────────────
        // ARM64 agent pulls the multi-arch manifest → gets linux/arm64 layer.
        // Windows agent pulls with --platform linux/amd64 → gets linux/amd64 layer.
        stage('Test Docker Images') {
            parallel {
                stage('Docker Test: linux/arm64') {
                    agent { label 'linux-arm64' }
                    options { skipDefaultCheckout() }
                    environment {
                        PATH = '/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin'
                    }
                    steps {
                        cleanWs()
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
                            docker rm -f tm-docker-${BUILD_NUMBER} || true
                            [ "$HTTP" = "200" ] || (echo "Docker linux/arm64 smoke FAILED: HTTP $HTTP" && exit 1)
                            echo "Docker linux/arm64 smoke: PASSED (HTTP $HTTP)"
                        '''
                    }
                }
                stage('Docker Test: linux/amd64') {
                    agent { label 'windows' }
                    options { skipDefaultCheckout() }
                    steps {
                        cleanWs()
                        powershell '''
                            $img  = "$env:REGISTRY/$($env:IMAGE):$env:TAG"
                            $name = "tm-docker-amd64-$env:BUILD_NUMBER"
                            docker pull --platform linux/amd64 $img
                            docker run -d `
                              --platform linux/amd64 `
                              --name $name `
                              -e THE_MOMENT_DB_PATH=/app/data/db `
                              -e SPOOLMAN_URL=http://127.0.0.1:9999 `
                              -p 15201:5000 `
                              $img
                            $http = "000"
                            for ($i = 1; $i -le 20; $i++) {
                                Start-Sleep 2
                                try {
                                    $r = Invoke-WebRequest -Uri "http://localhost:15201/api/status" `
                                             -UseBasicParsing -TimeoutSec 2
                                    $http = [string]$r.StatusCode
                                } catch {}
                                if ($http -eq "200") { break }
                            }
                            Write-Host "=== Container logs ==="
                            docker logs $name 2>&1
                            docker rm -f $name | Out-Null
                            if ($http -ne "200") {
                                Write-Error "Docker linux/amd64 smoke FAILED: HTTP $http"
                                exit 1
                            }
                            Write-Host "Docker linux/amd64 smoke: PASSED (HTTP $http)"
                        '''
                    }
                }
            }
        }

        // ── Archive binaries ──────────────────────────────────────────────────
        // linux/amd64 is compiled inside the Docker image — no standalone artifact.
        stage('Archive') {
            agent { label 'linux-arm64' }
            options { skipDefaultCheckout() }
            environment {
                PATH = '/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin'
            }
            steps {
                cleanWs()
                unstash 'bin-linux-arm64'
                unstash 'bin-windows-amd64'
                sh '''
                    sha256sum \
                      the-moment-linux-arm64 \
                      the-moment-windows-amd64.exe \
                      > checksums.txt
                    cat checksums.txt
                '''
                archiveArtifacts artifacts: 'the-moment-linux-arm64, the-moment-windows-amd64.exe, checksums.txt',
                                  fingerprint: true
            }
        }
    }

    post {
        failure { echo 'Pipeline FAILED — check stage logs above.' }
        success {
            echo "Build complete. Multi-arch image at ${REGISTRY}/${IMAGE}:${TAG} (linux/arm64 + linux/amd64). Binaries: linux/arm64, windows/amd64."
        }
    }
}
