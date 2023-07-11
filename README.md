## kruise-game-open-match-director

```yaml
        command:
        - '/director'
        - '--game-server-label-selector=flappy-bird'
        - '--lease-lock-name=kruise-game-open-match-director'
        - '--lease-lock-namespace=open-match-demo'
        - '--match-function-endpoint=om-function.open-match-demo.svc.cluster.local'
        - '--match-function-port=50502'
```
game-server-label-selector用来匹配frontService中ticketPool的名字，也作为GameServer的匹配策略，GameServer上面额外配置`game.kruise.io/open-match-selector`为key的label进行匹配。