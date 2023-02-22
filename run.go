/*
	Program Name : ContainerManagerForCTFd
	Author : leejw
	language : go
	Date : 2023.2.24
*/

package main

import (
	"container/list"
	"database/sql"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// initial variable setting
const (
	Host  string = "127.0.0.1"
	SPort int    = 8888
)

// image struct 정의
type Image struct {
	image_id       string
	repo_tag       string
	challenge_id   int
	challenge_name string
	lifetime       int
}

type Container struct {
	container_id string
	image_id     string
	port         int
	start_time   time.Time
	session_id   string
	terminate    bool
}

var DataBase *sql.DB

func init() {
	db, err := InitDB("./ctf.db")
	if err != nil {
		log.Fatal(err)
	}
	DataBase = db
	// challenge별 container 이미지 정보 생성(container build 후 docker inspect 명령으로 정보 획득)
	AddImage(DataBase, Image{image_id: "b73f33df893b2fce40d845b298c5f29696298d9f1e226144c72c00c263fb41bf", repo_tag: "web_01:latest", challenge_id: 1, challenge_name: "TEST_Challenge", lifetime: 60}) // local ctfd web_01 문제
	AddImage(DataBase, Image{image_id: "a4cc40b2f5d86078b76210cf023472ed569409bb153a36ca4202bee3bd9458a0", repo_tag: "redletter:latest", challenge_id: 7, challenge_name: "REDLETTER", lifetime: 60})   //ctfd 개발환경 redletter 문제

	// image, err := GetImage(DataBase, "b73f33df893b2fce40d845b298c5f29696298d9f1e226144c72c00c263fb41bf")
	// fmt.Println(image.challenge_id)
}

// 생성 요청을 처리할 Queue 생성
type queue struct {
	v *list.List
}

func newQueue() *queue {
	return &queue{list.New()}
}

func (q *queue) push(v interface{}) {
	q.v.PushBack(v)
}

func (q *queue) pop() interface{} {
	front := q.v.Front()
	if front == nil {
		return nil
	}
	return q.v.Remove(front)
}

// 랜덤 포트 생성
func genPort() int {
	min := 10000
	max := 20000
	rand.Seed(time.Now().UnixNano())
	dockerPort := rand.Intn(max-min) + min
	return dockerPort
}

//
// 로그인 상태 확인
func checkSession(cookie string) int {
	req, err := http.NewRequest("GET", "http://"+Host+":4000/user", nil) // CTFd url
	if err != nil {
		panic(err)
	}
	req.Header.Add("Cookie", cookie) // session을 cookie로 설정
	req.Header.Add("User-Agent", "ctf-manager")

	// Client 선언 및 로그인 실패시 리다이렉션 되지 않도록 설정
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// challenge 컨테이너 실행요청을 받기 위한 web서버
func RunNewHttpServer() {
	AddActiveLog("[+] RunNewHttpServer called.") //logging

	addr := fmt.Sprintf(":%d", SPort)
	log.Printf("Server is listening on %d", SPort)
	//request queue 생성
	queue := newQueue()

	//container 생성 요청
	http.HandleFunc("/reqvm", func(w http.ResponseWriter, req *http.Request) {
		// 로그인 상태 확인
		cookie := req.Header.Get("Cookie")
		statusCode := checkSession(cookie)
		fmt.Println(req.Header)
		AddActiveLog("[+] " + cookie) //logging
		if statusCode == 200 {        // 로그인 세션이 있을 경우
			// 요청하는 문제 image id 확인
			imageId := req.URL.Query().Get("id")
			AddActiveLog("[+] request container : " + imageId) //logging
			queue.push(imageId)
			q_imageId := fmt.Sprintf("%v", queue.pop())

			// gorutine으로 container 실행
			sessionId := strings.Split(cookie, "=")[1]
			// [to-do] 동일 세션 ID로 동일한 문제 컨테이너 요청시 reject
			// func checkDuplication(q_imageId, sessionId)

			c := make(chan string)
			go runContainer(q_imageId, sessionId, c)
			challUrl := <-c

			if challUrl == "" {
				if _, err := w.Write([]byte("uuid is incorrect.\n" + challUrl)); err != nil {
					log.Print(err)
				}
			} else {
				if _, err := w.Write([]byte("container is started..\n" + challUrl)); err != nil {
					log.Print(err)
				}
			}
		} else { // 로그인 세션이 없을 경우
			if _, err := w.Write([]byte("login first...\n")); err != nil {
				log.Print(err)
			}
		}
	})

	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Print(err)
	}
}

// Container Job ================================================================ S
// 컨테이너 실행
func runContainer(imageId string, sessionId string, c chan string) {
	if imageId == "" {
		c <- ""
	} else {
		dockerPort := genPort()
		out, err := exec.Command("docker", "run", "--rm", "-d", "-p", strconv.Itoa(dockerPort)+":8000", imageId).Output()
		if err != nil {
			log.Println(err.Error())
		}
		containerId := strings.TrimRight(string(out[:]), "\n")
		container := Container{container_id: containerId, image_id: imageId, port: dockerPort, start_time: time.Now(), session_id: sessionId, terminate: false}
		AddContainer(container) // add container to db
		url := "http://" + Host + ":" + strconv.Itoa(dockerPort)
		log.Println("[+] container " + containerId + " is started (" + url + ")")

		c <- url
	}
}

// 실행중인 컨테이너를 중지 및 삭제함
func stopContainers() {
	for true {
		containers, _ := GetRunningContainers()
		time.Sleep(10 * time.Second) // container 실행 시간 시뮬레이션(3초 소요 설정)

		for _, container := range containers {
			containerId := container.container_id
			currentTime := time.Now()
			startTime := container.start_time
			runningTime, _ := time.ParseDuration(currentTime.Sub(startTime).String())
			log.Println("[+] container " + containerId + " is running " + fmt.Sprintf("%f", runningTime.Seconds()) + " seconds")
			// [to-do] 현재 image의 life time을 가져오도록 설정
			// 현재 모두 60초 지난 컨테이너는 삭제 하는것으로 고정 --> 시간조정
			if runningTime.Seconds() > 60 {
				go func() {
					log.Println("[+] remove container start: " + containerId)
					out, err := exec.Command("docker", "stop", containerId).Output()
					if err != nil {
						log.Print(err)
					}
					output := string(out[:])
					UpdateContainerStatus(containerId)
					log.Println("[+] remove container done: " + strings.TrimRight(output, "\n"))
				}()
			}
		}
	}
}

// 실행중인 컨테이너를 중지 및 삭제함
func stopContainers_bak() {
	for true {
		containers, _ := GetRunningContainers()
		time.Sleep(10 * time.Second) // container 실행 시간 시뮬레이션(3초 소요 설정)

		for _, container := range containers {
			containerId := container.container_id
			currentTime := time.Now()
			startTime := container.start_time
			runningTime, _ := time.ParseDuration(currentTime.Sub(startTime).String())
			log.Println("[+] container " + containerId + " is running " + fmt.Sprintf("%f", runningTime.Seconds()) + " seconds")
			// [to-do] 현재 image의 life time을 가져오도록 설정
			// 현재 모두 60초 지난 컨테이너는 삭제 하는것으로 고정 --> 시간조정
			if runningTime.Seconds() > 60 {
				log.Println("[+] remove container start: " + containerId)
				out, err := exec.Command("docker", "stop", containerId).Output()
				if err != nil {
					log.Print(err)
				}
				output := string(out[:])
				UpdateContainerStatus(containerId)
				log.Println("[+] remove container done: " + strings.TrimRight(output, "\n"))
			}
		}
	}
}

// Container Job ================================================================ E

// DB Job ======================================================================= S
func InitDB(file string) (*sql.DB, error) {
	// os.Remove(file)

	db, err := sql.Open("sqlite3", file)
	if err != nil {
		return nil, err
	}

	createTableQuery := `
		create table IF NOT EXISTS image (
			id integer PRIMARY KEY autoincrement,
			image_id       text,
			repo_tag       text,
			challenge_id   integer,
			challenge_name text,
			life_time      integer,
			UNIQUE (id, image_id)
		);
		create table IF NOT EXISTS container (
			id integer PRIMARY KEY autoincrement,
			container_id text,
			image_id     text,
			port         integer,
			start_time   datetime,
			session_id   text,
			terminate    boolean,
			UNIQUE (id, container_id)
		);
		create table IF NOT EXISTS logs (
			id integer PRIMARY KEY autoincrement,
			time    datetime,
			message text,
			UNIQUE (id)
		);
	`
	_, e := db.Exec(createTableQuery)
	if e != nil {
		return nil, e
	}

	return db, nil
}

// 이미지(Challenge image) 정보 생성, Initial 함수에서 호출하여 데이터 생성
func AddImage(db *sql.DB, image Image) error {
	tx, _ := db.Begin()
	query := `insert into image (image_id, repo_tag, challenge_id, challenge_name, life_time) values (?,?,?,?,?)`
	stmt, err := tx.Prepare(query)
	if err != nil {
		log.Println(err.Error())
		return err
	}
	_, err = stmt.Exec(image.image_id, image.repo_tag, image.challenge_id, image.challenge_name, image.lifetime)
	if err != nil {
		log.Println(err.Error())
		return err
	}
	tx.Commit()
	return nil
}

// Image id에 해당하는 이미지 정보 조회
func GetImage(db *sql.DB, imageId string) (Image, error) {
	image := Image{}
	query := `select image_id, repo_tag, challenge_id, challenge_name, life_time from image where image_id = $1`
	row := db.QueryRow(query, imageId)

	err := row.Scan(&image.image_id, &image.repo_tag, &image.challenge_id, &image.challenge_name, &image.lifetime)
	if err != nil {
		log.Println(err.Error())
		return Image{}, err

	}
	return image, nil
}

// 컨테이너 실행 결과 입력
func AddContainer(container Container) error {
	tx, _ := DataBase.Begin()
	stmt, _ := tx.Prepare("insert into container (container_id, image_id, port, start_time, session_id, terminate) values (?,?,?,?,?,?)")
	_, err := stmt.Exec(container.container_id, container.image_id, container.port, container.start_time, container.session_id, container.terminate)
	if err != nil {
		log.Println(err.Error())
		return err
	}
	tx.Commit()
	return nil
}

// 실행중인 컨테이너 목록 조회
func GetRunningContainers() (containers []Container, err error) {
	query := `select container_id, start_time from container where terminate = 0 order by start_time`
	rows, err := DataBase.Query(query)
	if err != nil {
		log.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		container := Container{}
		err := rows.Scan(&container.container_id, &container.start_time)
		if err != nil {
			log.Fatal(err)
			return containers, err
		}
		containers = append(containers, container)
	}
	return containers, nil
}

func UpdateContainerStatus(containerId string) error {
	tx, _ := DataBase.Begin()
	stmt, _ := tx.Prepare("update container set terminate = true where container_id = ?")
	_, err := stmt.Exec(containerId)
	if err != nil {
		log.Println(err.Error())
		return err
	}
	tx.Commit()
	return nil
}

func AddActiveLog(message string) {
	tx, _ := DataBase.Begin()
	stmt, _ := tx.Prepare("insert into logs (time, message) values (?,?)")
	_, err := stmt.Exec(time.Now(), message)
	if err != nil {
		log.Println(err.Error())
	}
	tx.Commit()
}

// DB Job ======================================================================= E
func main() {
	go RunNewHttpServer()
	stopContainers()

}
