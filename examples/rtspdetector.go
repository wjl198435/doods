package main

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"io"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"sync"

	"github.com/hybridgroup/mjpeg"
	"gocv.io/x/gocv"
	"google.golang.org/grpc"

	"github.com/snowzach/doods/odrpc"
)

var (
	deviceID int
	err      error
	capture  *gocv.VideoCapture
	stream   *mjpeg.Stream
)

func main() {
	if len(os.Args) < 5 {
		fmt.Println("How to run:\n\trtspdetector [source url] [host:port] [doods server] [detector]")
		return
	}

	// parse args
	source := os.Args[1]
	host := os.Args[2]
	server := os.Args[3]
	detector := os.Args[4]

	// open webcam
	capture, err = gocv.OpenVideoCapture(source)
	if err != nil {
		fmt.Printf("Error opening capture device: %v: %v\n", source, err)
		return
	}
	defer capture.Close()

	// create the mjpeg stream
	stream = mjpeg.NewStream()

	// start capturing
	go mjpegCapture(server, detector)

	fmt.Println("Capturing. Point your browser to " + host)

	// start http server
	http.Handle("/", stream)
	log.Fatal(http.ListenAndServe(host, nil))
}

func mjpegCapture(server string, detector string) {

	dialOptions := []grpc.DialOption{
		grpc.WithBlock(),
		grpc.WithInsecure(),
	}

	// Set up a connection to the gRPC server.
	conn, err := grpc.Dial(server, dialOptions...)
	if err != nil {
		log.Fatalf("Could not connect: %v", err)
	}
	defer conn.Close()

	// gRPC version Client
	client := odrpc.NewOdrpcClient(conn)
	detectStream, err := client.DetectStream(context.Background())
	if err != nil {
		log.Fatalf("Could not stream: %v", err)
	}

	img := gocv.NewMat()
	defer img.Close()
	detectImg := gocv.NewMat()
	defer detectImg.Close()

	// color for the rect when faces detected
	green := color.RGBA{0, 255, 0, 0}
	var rs = make([]image.Rectangle, 0)
	var labels = make([]string, 0)
	var confidences = make([]float32, 0)
	var m sync.Mutex
	var detectorReady bool = true

	for {
		if ok := capture.Read(&img); !ok {
			fmt.Printf("Device closed: %v\n", deviceID)
			return
		}
		if img.Empty() {
			continue
		}

		request := &odrpc.DetectRequest{
			DetectorName: detector,
			Detect: map[string]float32{
				"*": 60, //
			},
		}

		gocv.Resize(img.Region(image.Rectangle{Min: image.Point{X: 0, Y: 0}, Max: image.Point{X: 1080, Y: 1080}}), &detectImg, image.Point{X: 300, Y: 300}, 0.0, 0.0, gocv.InterpolationNearestNeighbor)
		request.Data, err = gocv.IMEncode(".bmp", detectImg)
		if err != nil {
			continue
		}

		m.Lock()
		if detectorReady {
			detectorReady = false
			if err := detectStream.Send(request); err != nil {
				log.Fatalf("could not stream send %v", err)
			}
			go func() {
				response, err := detectStream.Recv()
				if err == io.EOF {
					log.Fatalf("can not receive %v", err)
				}
				if err != nil {
					log.Fatalf("can not receive %v", err)
				}
				log.Printf("Processed: %v", response)

				m.Lock()
				detections := len(response.Detections)
				rs = make([]image.Rectangle, detections, detections)
				labels = make([]string, detections, detections)
				confidences = make([]float32, detections, detections)
				for x := 0; x < detections; x++ {
					rs[x] = image.Rectangle{
						Min: image.Point{X: int(response.Detections[x].Left * 1080), Y: int(response.Detections[x].Top * 1080)},
						Max: image.Point{X: int(response.Detections[x].Right * 1080), Y: int(response.Detections[x].Bottom * 1080)},
					}
					labels[x] = response.Detections[x].Label
					confidences[x] = response.Detections[x].Confidence
				}
				detectorReady = true
				m.Unlock()
			}()
		}
		m.Unlock()

		for x := 0; x < len(rs); x++ {
			gocv.Rectangle(&img, rs[x], green, 1)
			pt := image.Pt(rs[x].Min.X, rs[x].Min.Y-2)
			gocv.PutText(&img, fmt.Sprintf("%s %0.0f", labels[x], confidences[x]), pt, gocv.FontHersheyPlain, 1.5, green, 1)
		}

		// re-encode with boxes
		request.Data, err = gocv.IMEncode(".jpg", img)
		if err != nil {
			continue
		}

		// buf, _ := gocv.IMEncode(".jpg", img)
		stream.UpdateJPEG(request.Data)

	}
}
