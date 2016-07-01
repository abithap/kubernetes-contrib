# High Available Multi-Backend Load balancer

The project implements a load balancer controller that will provide a high available and load balancing access to HTTP and TCP kubernetes applications. It also provide SSL support for http apps.

Our goal is to have this controller listen to ingress events, rather than config map for generating config rules. Currently, this controller watches for configmap resources
to create and configure backend. Eventually, this will be changed to watch for ingress resource instead. This feature is still being planned in kubernetes since the current version
of ingress does not support layer 4 routing.

This controller is designed to easily integrate and create different load balancing backends. From software, hardware to cloud loadbalancer. Our initial featured backends are software loadbalacing (with keepalived and nginx) and Openstack LBaaS v2 (Octavia) and .

In the case for software loadbalacer, this controllers work with loadbalancer-controller daemons which are deployed across nodes which will servers as high available loadbalacers. These daemon controllers use keepalived and nginx to provide 
the high availability loadbalancing via the use of VIPs. The loadbalance controller will communicate with the daemons via a configmap resource.

**Note**: The daemon needs to run in priviledged mode and with `hostNetwork: true` so that it has access to the underlying node network. This is needed so that the VIP can be assigned to the node interfaces so that they are accessible externally.

## Examples

### Software Loadbalancer using keepalived and nginx

1. First we need to create the loadbalancer controller.
  ```
  $ kubectl create -f example/ingress-loadbalancer-rc.yaml
  ```

1. The loadbalancer daemon pod will only start in nodes that are labeled `type: loadbalancer`. Label the nodes you want the daemon to run on
  ```
  $ kubectl label node my-node1 type=loadbalancer
  ```

1. Create our sample app, which consists of a service and replication controller resource:

  ```
  $ kubectl create -f examples/coffee-app.yaml
  ```

1. Create configmap for the sample app service. This will be used to configure the loadbalancer backend:
  ```
  $ kubectl create -f coffee-configmap.yaml
  ```

1. Get the bind IP generated by the loadbalancer controller from the configmap.
  ```
  $ kubectl get configmap configmap-coffee-svc -o yaml
  apiVersion: v1
  data:
    bind-ip: "10.0.0.10"
    bind-port: "80"
    namespace: default
    target-service-name: coffee-svc
  kind: ConfigMap
  metadata:
    creationTimestamp: 2016-06-17T22:30:03Z
    labels:
      app: loadbalancer
    name: configmap-coffee-svc
    namespace: default
    resourceVersion: "157728"
    selfLink: /api/v1/namespaces/default/configmaps/configmap-coffee-svc
    uid: 08e12303-34db-11e6-87da-fa163eefe713
  ```

1. To get coffee:
  ```
    $ curl http://10.0.0.10
    <!DOCTYPE html>
    <html>
    <head>
    <title>Hello from NGINX!</title>
    <style>
        body {
            width: 35em;
            margin: 0 auto;
            font-family: Tahoma, Verdana, Arial, sans-serif;
        }
    </style>
    </head>
    <body>
    <h1>Hello!</h1>
    <h2>URI = /coffee</h2>
    <h2>My hostname is coffee-rc-mu9ns</h2>
    <h2>My address is 10.244.0.3:80</h2>
    </body>
    </html>
  ```

### Cloud Load Balancing (Openstack LBaaS V2)

1. First we need to create the loadbalancer controller. You can specify the type of backend used for the loadbalancer via an environment variable. If using openstack loadbalancer, provide your Openstack information as an environment variables. The password is supplied via a secret resource. 
  ```
  $ kubectl create -f example/ingress-loadbalancer-rc-openstack.yaml
  ```

1. Create our sample app, which consists of a service and replication controller resource. Since Openstack LBaaS needs to access your apps, make sure your application is deployed with `type: NodePort`:

  ```
  $ kubectl create -f examples/coffee-app.yaml
  ```

1. Create configmap for the sample app service. This will be used to configure the loadbalancer backend:
  ```
  $ kubectl create -f coffee-configmap.yaml
  ```

1. Get the bind IP generated by the loadbalancer controller from the configmap. The bind IP should be the VIP generated by Openstack LBaaS
  ```
  $ kubectl get configmap configmap-coffee-svc -o yaml
  apiVersion: v1
  data:
    bind-ip: "10.0.0.81"
    bind-port: "80"
    namespace: default
    target-service-name: coffee-svc
  kind: ConfigMap
  metadata:
    creationTimestamp: 2016-06-17T22:30:03Z
    labels:
      app: loadbalancer
    name: configmap-coffee-svc
    namespace: default
    resourceVersion: "157728"
    selfLink: /api/v1/namespaces/default/configmaps/configmap-coffee-svc
    uid: 08e12303-34db-11e6-87da-fa163eefe713
  ```

1. Curl the VIP to access the coffee app
  ```
  $ curl http://10.0.0.81
  <!DOCTYPE html>
  <html>
  <head>
  <title>Hello from NGINX!</title>
  <style>
      body {
          width: 35em;
          margin: 0 auto;
          font-family: Tahoma, Verdana, Arial, sans-serif;
      }
  </style>
  </head>
  <body>
  <h1>Hello!</h1>
  <h2>URI = /coffee</h2>
  <h2>My hostname is coffee-rc-auqj8</h2>
  <h2>My address is 172.18.99.3:80</h2>
  </body>
  </html>
  ```

1. The apps are accessed via a nodePort in the K8 nodes which is in the range of 30000-32767. Make sure they are open in the nodes. Also make sure to open up any ports that bind to the load balancer, such as port 80 in this case.

**Note**: Implementations are experimental and not suitable for using in production. This project is still in its early stage and many things are still in work in progress.