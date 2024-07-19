Copy config.example.yaml to config.yaml and update values accordingly

If you changed the listen address, update the port forward in docker-compose.yaml

Run `docker-compose up --build` to start the app

Hit the app over HTTP passing your plex username and the start/end times of the clip in the request

```sh
curl http://127.0.0.1:8080/clip/ahorner/00:05:00/00:05:05 -O -J
```
