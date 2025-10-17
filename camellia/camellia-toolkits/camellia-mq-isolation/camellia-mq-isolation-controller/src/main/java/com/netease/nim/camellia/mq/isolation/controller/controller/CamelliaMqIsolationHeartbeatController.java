package com.netease.nim.camellia.mq.isolation.controller.controller;

import com.netease.nim.camellia.mq.isolation.controller.service.HeartbeatService;
import com.netease.nim.camellia.mq.isolation.core.domain.ConsumerHeartbeat;
import com.netease.nim.camellia.mq.isolation.core.domain.SenderHeartbeat;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.web.bind.annotation.*;

import java.util.List;

/**
 * Created by caojiajun on 2024/2/20
 */
@RestController
@RequestMapping("/camellia/mq/isolation/heartbeat")
public class CamelliaMqIsolationHeartbeatController {

    @Autowired
    private HeartbeatService heartbeatService;

    @RequestMapping(value = "/senderHeartbeat", method = RequestMethod.POST)
    public WebResult senderHeartbeat(@RequestBody SenderHeartbeat heartbeat) {
        CamelliaMqIsolationControllerStatus.updateLastUseTime();
        heartbeatService.senderHeartbeat(heartbeat);
        return WebResult.success();
    }

    @RequestMapping(value = "/consumerHeartbeat", method = RequestMethod.POST)
    public WebResult consumerHeartbeat(@RequestBody ConsumerHeartbeat heartbeat) {
        CamelliaMqIsolationControllerStatus.updateLastUseTime();
        heartbeatService.consumerHeartbeat(heartbeat);
        return WebResult.success();
    }

    @RequestMapping(value = "/querySenderHeartbeat", method = RequestMethod.GET)
    public WebResult querySenderHeartbeat(@RequestParam(name = "namespace") String namespace) {
        CamelliaMqIsolationControllerStatus.updateLastUseTime();
        List<SenderHeartbeat> list = heartbeatService.querySenderHeartbeat(namespace);
        return WebResult.success(list);
    }

    @RequestMapping(value = "/queryConsumerHeartbeat", method = RequestMethod.GET)
    public WebResult queryConsumerHeartbeat(@RequestParam(name = "namespace") String namespace) {
        CamelliaMqIsolationControllerStatus.updateLastUseTime();
        List<ConsumerHeartbeat> list = heartbeatService.queryConsumerHeartbeat(namespace);
        return WebResult.success(list);
    }
}
