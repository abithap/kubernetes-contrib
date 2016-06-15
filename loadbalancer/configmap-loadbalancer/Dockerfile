FROM gcr.io/google_containers/ubuntu-slim:0.2
RUN apt-get update
RUN apt-get install -y --no-install-recommends \
  nginx \
  gettext-base \
  ca-certificates && \
  apt-get clean && \
  rm -rf /var/lib/apt/lists/*

# forward nginx access and error logs to stdout and stderr of the configmap
# controller process
RUN ln -sf /proc/1/fd/1 /var/log/nginx/access.log \
	&& ln -sf /proc/1/fd/2 /var/log/nginx/error.log

COPY configmap-loadbalancer /
COPY backends/nginx/nginx.tmpl /

ENTRYPOINT ["/configmap-loadbalancer"]