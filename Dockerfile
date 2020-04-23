FROM alpine
ADD ./dist /opt
RUN sed -i 's/dl-cdn.alpinelinux.org/mirrors.aliyun.com/' /etc/apk/repositories \
  && apk add tzdata&& rm -f /var/cache/apk/* \
  && cp -f /usr/share/zoneinfo/PRC /etc/localtime
ENTRYPOINT /opt/gateway.server
HEALTHCHECK --interval=10s --timeout=3s CMD /opt/gateway.server check
