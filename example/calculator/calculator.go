package main

import (
	"example/nicepb/nice"

	"github.com/asynkron/protoactor-go/cluster"
	"github.com/murang/potato/log"
)

type CalculatorImpl struct{}

func (c CalculatorImpl) Init(ctx cluster.GrainContext) {
	log.Sugar.Infof("CalculatorImpl init")
}

func (c CalculatorImpl) Terminate(ctx cluster.GrainContext) {
	log.Sugar.Infof("CalculatorImpl terminate")
}

func (c CalculatorImpl) ReceiveDefault(ctx cluster.GrainContext) {
	log.Sugar.Infof("CalculatorImpl receive default")
}

func (c CalculatorImpl) Sum(req *nice.Input, ctx cluster.GrainContext) (*nice.Output, error) {
	return &nice.Output{Result: req.A + req.B}, nil
}
