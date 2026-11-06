package dockerapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const testID = "aaaaaaaaaaaa1111111111112222222222223333333333334444444444445555"

func TestRejectsAnythingButAllowedGET(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()
	client := newClient(srv.URL, http.DefaultTransport)

	cases := []struct{ method, path string }{
		{http.MethodPost, "/containers/" + testID + "/start"},
		{http.MethodPost, "/containers/" + testID + "/stop"},
		{http.MethodPost, "/containers/" + testID + "/restart"},
		{http.MethodPost, "/containers/" + testID + "/kill"},
		{http.MethodPost, "/containers/" + testID + "/exec"},
		{http.MethodPost, "/containers/create"},
		{http.MethodDelete, "/containers/" + testID},
		{http.MethodPut, "/containers/" + testID + "/archive"},
		{http.MethodPost, "/containers/json"},
		{http.MethodGet, "/containers/" + testID + "/archive"},
		{http.MethodGet, "/images/json"},
		{http.MethodGet, "/containers/../images/json"},
		{http.MethodGet, "/containers/" + testID + "/json/../exec"},
		{http.MethodGet, "/v1.45/containers/" + testID + "/exec"},
		{http.MethodGet, "/secrets"},
	}
	for _, tc := range cases {
		req, err := http.NewRequest(tc.method, srv.URL+tc.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.http.Do(req)
		if !errors.Is(err, ErrNotAllowed) {
			t.Errorf("%s %s: err = %v, want ErrNotAllowed", tc.method, tc.path, err)
		}
	}
	if got := hits.Load(); got != 0 {
		t.Errorf("Docker API에 %d개의 요청이 도달했다", got)
	}
}

func TestDoesNotFollowRedirectToMutatingPath(t *testing.T) {
	var postHits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_ping" {
			http.Redirect(w, r, "/containers/"+testID+"/stop", http.StatusFound)
			return
		}
		postHits.Add(1)
	}))
	defer srv.Close()

	client := newClient(srv.URL, http.DefaultTransport)
	if err := client.Ping(context.Background()); err == nil {
		t.Error("리다이렉트 응답을 성공으로 처리했다")
	}
	if postHits.Load() != 0 {
		t.Error("리다이렉트를 따라갔다")
	}
}

func fakeDocker(t *testing.T, inspect string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /containers/json", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("all") != "true" {
			t.Error("중지된 컨테이너를 포함하지 않았다")
		}
		w.Write([]byte(`[{"Id":"` + testID + `"}]`))
	})
	mux.HandleFunc("GET /containers/"+testID+"/json", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(inspect))
	})
	mux.HandleFunc("GET /networks", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`[{"Id":"n1","Name":"blog_default","Driver":"bridge","IPAM":{"Config":[{"Subnet":"172.20.0.0/16"}]},
			"Options":{"secret-option":"NETWORK-SECRET"}}]`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

const inspectWithSecrets = `{
  "Id": "` + testID + `",
  "Name": "/blog-api-1",
  "Created": "2026-11-06T01:00:00Z",
  "Image": "sha256:abc",
  "RestartCount": 2,
  "Path": "/bin/server",
  "Args": ["--password=ARG-SECRET"],
  "State": {"Status":"running","ExitCode":0,"OOMKilled":false,"StartedAt":"2026-11-06T02:00:00Z","Health":{"Status":"unhealthy"}},
  "Config": {
    "Image": "registry.local/blog-api:1.2",
    "Env": ["DB_PASSWORD=ENV-SECRET", "API_KEY=ENV-SECRET-2"],
    "Cmd": ["/bin/server", "--token=CMD-SECRET"],
    "Labels": {
      "com.docker.compose.project": "blog",
      "com.docker.compose.service": "api",
      "com.docker.compose.container-number": "1",
      "com.docker.compose.depends_on": "db:service_started:false,cache:service_healthy:true",
      "traefik.http.middlewares.auth.basicauth.users": "admin:LABEL-SECRET"
    }
  },
  "HostConfig": {"RestartPolicy":{"Name":"always"},"Binds":["/secret/path:/data"]},
  "NetworkSettings": {
    "Networks": {"blog_default":{"IPAddress":"172.20.0.3"},"proxy":{"IPAddress":"172.21.0.5"}},
    "Ports": {
      "8080/tcp": [{"HostIp":"127.0.0.1","HostPort":"18080"}],
      "9090/tcp": null
    }
  }
}`

func TestTopologyNeverContainsSecrets(t *testing.T) {
	srv := fakeDocker(t, inspectWithSecrets)
	client := newClient(srv.URL, http.DefaultTransport)

	topology, err := client.Topology(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(topology)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"SECRET", "/secret/path", "traefik"} {
		if strings.Contains(string(body), secret) {
			t.Errorf("스냅샷에 %q가 포함되어 있다: %s", secret, body)
		}
	}
}

func TestTopologyMapsDockerState(t *testing.T) {
	srv := fakeDocker(t, inspectWithSecrets)
	client := newClient(srv.URL, http.DefaultTransport)

	topology, err := client.Topology(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(topology.Containers) != 1 {
		t.Fatalf("containers = %d", len(topology.Containers))
	}
	c := topology.Containers[0]
	if c.ServiceKey() != "blog/api" || c.Replica != 1 {
		t.Errorf("서비스 식별 실패: %s #%d", c.ServiceKey(), c.Replica)
	}
	if c.State != "running" || c.Health != "unhealthy" || c.RestartCount != 2 {
		t.Errorf("상태 변환 실패: %+v", c)
	}
	if got := strings.Join(c.DeclaredDependsOn, ","); got != "cache,db" {
		t.Errorf("선언 의존 = %q", got)
	}
	if len(c.Networks) != 2 || c.Networks[0].Network != "blog_default" {
		t.Errorf("네트워크 = %+v", c.Networks)
	}
	if len(c.Ports) != 2 {
		t.Fatalf("ports = %+v", c.Ports)
	}
	if p := c.Ports[0]; p.HostIP != "127.0.0.1" || p.HostPort != 18080 || p.ContainerPort != 8080 {
		t.Errorf("공개 포트 = %+v", p)
	}
	if p := c.Ports[1]; p.HostPort != 0 || p.ContainerPort != 9090 {
		t.Errorf("미공개 포트가 공개 포트로 표시됐다: %+v", p)
	}
	if topology.Hash == "" {
		t.Error("해시가 비어 있다")
	}
}

func TestStandaloneContainerUsesNameAsService(t *testing.T) {
	inspect := `{"Id":"` + testID + `","Name":"/minio","State":{"Status":"exited","ExitCode":137},"Config":{"Image":"minio"},"NetworkSettings":{}}`
	srv := fakeDocker(t, inspect)
	client := newClient(srv.URL, http.DefaultTransport)

	topology, err := client.Topology(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	c := topology.Containers[0]
	if c.ServiceKey() != "_standalone/minio" || c.Health != "none" || c.ExitCode != 137 {
		t.Errorf("c = %+v", c)
	}
}

func TestContainerRemovedDuringListingIsSkipped(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /containers/json", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`[{"Id":"` + testID + `"}]`))
	})
	mux.HandleFunc("GET /containers/"+testID+"/json", func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	})
	mux.HandleFunc("GET /networks", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`[]`)) })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	topology, err := newClient(srv.URL, http.DefaultTransport).Topology(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(topology.Containers) != 0 {
		t.Errorf("containers = %+v", topology.Containers)
	}
}
